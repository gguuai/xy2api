package service

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/liulixin-lex/xy2api/internal/scheduling"
	"github.com/stretchr/testify/require"
)

func TestControlledNonstreamSuccessSeparatesAvailabilityFromLatency(t *testing.T) {
	tests := []struct {
		name           string
		protocol       string
		body           string
		valid, healthy bool
	}{
		{name: "anthropic message", protocol: "messages", body: `{"type":"message","content":[],"stop_reason":"end_turn"}`, valid: true, healthy: true},
		{name: "responses completed", protocol: "responses", body: `{"status":"completed","output":[]}`, valid: true, healthy: true},
		{name: "responses incomplete is terminal but not healthy", protocol: "responses", body: `{"status":"incomplete","output":[]}`, valid: true, healthy: false},
		{name: "responses SSE incomplete is terminal but not healthy", protocol: "responses", body: "event: response.incomplete\ndata: {\"type\":\"response.incomplete\"}\n\n", valid: true, healthy: false},
		{name: "responses SSE terminal without delta", protocol: "responses", body: "data: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\",\"output\":[]}}\n\n", valid: true, healthy: true},
		{name: "responses SSE bare DONE lacks output evidence", protocol: "responses", body: "data: [DONE]\n\n", valid: false, healthy: false},
		{name: "responses SSE missing completed envelope", protocol: "responses", body: "data: {\"type\":\"response.completed\"}\n\n", valid: false, healthy: false},
		{name: "responses SSE delta then DONE", protocol: "responses", body: "data: {\"type\":\"response.output_text.delta\",\"delta\":\"ok\"}\n\ndata: [DONE]\n\n", valid: true, healthy: true},
		{name: "responses SSE malformed terminal", protocol: "responses", body: "data: {\"type\":\"response.completed\",\"response\":{\"output\":[]}\n\n", valid: false, healthy: false},
		{name: "chat completed", protocol: "chat", body: `{"choices":[{"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`, valid: true, healthy: true},
		{name: "gemini blocked is terminal but not healthy", protocol: "gemini", body: `{"promptFeedback":{"blockReason":"SAFETY"}}`, valid: true, healthy: false},
		{name: "count tokens", protocol: "count_tokens", body: `{"input_tokens":0}`, valid: true, healthy: true},
		{name: "count tokens exponent", protocol: "count_tokens", body: `{"input_tokens":1e2}`, valid: true, healthy: true},
		{name: "count tokens fractional", protocol: "count_tokens", body: `{"input_tokens":1.5}`, valid: false, healthy: false},
		{name: "count tokens negative fractional", protocol: "count_tokens", body: `{"input_tokens":-0.5}`, valid: false, healthy: false},
		{name: "count tokens overflow", protocol: "count_tokens", body: `{"input_tokens":1e100}`, valid: false, healthy: false},
		{name: "embeddings", protocol: "embeddings", body: `{"object":"list","data":[{"object":"embedding","embedding":[0.1],"index":0}]}`, valid: true, healthy: true},
		{name: "base64 embeddings", protocol: "embeddings", body: `{"data":[{"embedding":"zczMPQ==","index":0}]}`, valid: true, healthy: true},
		{name: "images", protocol: "images", body: `{"created":1700000000,"data":[{"url":"https://example.test/image.png"}]}`, valid: true, healthy: true},
		{name: "embeddings missing data", protocol: "embeddings", body: `{"object":"list"}`, valid: false, healthy: false},
		{name: "images malformed data", protocol: "images", body: `{"data":[null]}`, valid: false, healthy: false},
		{name: "images empty object", protocol: "images", body: `{"data":[{}]}`, valid: false, healthy: false},
		{name: "images empty output", protocol: "images", body: `{"data":[{"b64_json":" "}]}`, valid: false, healthy: false},
		{name: "images malformed output type", protocol: "images", body: `{"data":[{"url":123}]}`, valid: false, healthy: false},
		{name: "images partial output", protocol: "images", body: `{"data":[{"b64_json":"AQID"},{}]}`, valid: false, healthy: false},
		{name: "grok images empty object", protocol: "grok_images", body: `{"data":[{}]}`, valid: false, healthy: false},
		{name: "embeddings empty object", protocol: "embeddings", body: `{"data":[{}]}`, valid: false, healthy: false},
		{name: "embeddings empty vector", protocol: "embeddings", body: `{"data":[{"embedding":[]}]}`, valid: false, healthy: false},
		{name: "embeddings nonnumeric vector", protocol: "embeddings", body: `{"data":[{"embedding":[0.1,null]}]}`, valid: false, healthy: false},
		{name: "alpha search no hits", protocol: "alpha_search", body: `{"results":[]}`, valid: true, healthy: true},
		{name: "alpha search encrypted", protocol: "alpha_search", body: `{"encrypted_output":"ciphertext"}`, valid: true, healthy: true},
		{name: "alpha search empty output", protocol: "alpha_search", body: `{"output":""}`, valid: true, healthy: true},
		{name: "alpha search unrelated object", protocol: "alpha_search", body: `{"foo":"bar"}`, valid: false, healthy: false},
		{name: "grok voice text", protocol: "voice_json", body: `{"text":"recognized"}`, valid: true, healthy: true},
		{name: "grok voice silence", protocol: "voice_json", body: `{"text":""}`, valid: true, healthy: true},
		{name: "grok custom voice", protocol: "voice_json", body: `{"voice_id":"voice-1"}`, valid: true, healthy: true},
		{name: "grok voice timestamp audio", protocol: "voice_json", body: `{"audio":"AQID","audio_timestamps":{"graph_chars":[],"graph_times":[]}}`, valid: true, healthy: true},
		{name: "grok voice unrelated object", protocol: "voice_json", body: `{"foo":"bar"}`, valid: false, healthy: false},
		{name: "grok media task", protocol: "grok_video_create", body: `{"request_id":"video-1"}`, valid: true, healthy: true},
		{name: "grok proxy task", protocol: "grok_video_create", body: `{"task_id":"video-1"}`, valid: true, healthy: true},
		{name: "grok nested task", protocol: "grok_video_create", body: `{"data":{"task_id":"video-1"}}`, valid: true, healthy: true},
		{name: "seedance task", protocol: "seedance_create", body: `{"id":"task-1"}`, valid: true, healthy: true},
		{name: "opaque audio", protocol: "voice_audio", body: `audio`, valid: true, healthy: true},
		{name: "html is invalid", protocol: "messages", body: `<html>gateway error</html>`, valid: false, healthy: false},
		{name: "provider error object is invalid", protocol: "responses", body: `{"error":{"type":"server_error"}}`, valid: false, healthy: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			valid, healthy := controlledNonstreamSuccess([]byte(tt.body), tt.protocol)
			require.Equal(t, tt.valid, valid)
			require.Equal(t, tt.healthy, healthy)
		})
	}
}

func TestControlledNonstreamLargeTerminalEvidence(t *testing.T) {
	image := strings.Repeat("A", openAIFirstOutputStageMaxBytes+4)
	body := []byte("data: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\",\"output\":[{\"type\":\"image_generation_call\",\"result\":\"" + image + "\"}]}}\n\n")
	valid, healthy := controlledNonstreamSuccess(body, "responses")
	require.True(t, valid)
	require.True(t, healthy, "accepted image terminal evidence must not depend on the latency frame cap")
	failure := []byte("data: {\"type\":\"response.failed\",\"error\":{\"message\":\"" + image + "\"}}\n\n")
	valid, healthy = controlledNonstreamSuccess(append(body, failure...), "responses")
	require.True(t, valid)
	require.False(t, healthy, "large failure frames must also revoke success evidence")
}

func TestValidateControlledBufferedSSETerminalRequiresEvidence(t *testing.T) {
	for _, tc := range []struct {
		name                                   string
		enabled, buffered, rejected, validated bool
	}{
		{"enabled_buffered", true, true, true, false},
		{"legacy_disabled", false, true, false, true},
		{"streaming_owned", true, false, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := &ControlledRequest{Policy: scheduling.Policy{Enabled: tc.enabled}}
			d := &controlledDispatch{request: r}
			b := &controlledResponseBody{dispatch: d, success: true, sse: true, buffered: tc.buffered}
			resp := &http.Response{StatusCode: http.StatusOK, Body: b}
			err := validateControlledNonstreamResponse(resp, []byte("data: {\"type\":\"response.completed\"}\n\n"), "responses")
			if tc.rejected {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
			require.Equal(t, tc.validated, b.nonstreamValidated)
		})
	}
}

func TestValidateControlledNonstreamResponseRecordsInvalidCompletion(t *testing.T) {
	r := &ControlledRequest{Policy: scheduling.Policy{Enabled: true}}
	d := &controlledDispatch{request: r}
	b := &controlledResponseBody{dispatch: d, success: true, ReadCloser: io.NopCloser(strings.NewReader("<html>error</html>"))}
	resp := &http.Response{StatusCode: http.StatusOK, Body: b}

	err := validateControlledNonstreamResponse(resp, []byte("<html>error</html>"), "messages")
	require.Error(t, err)
	d.mu.Lock()
	require.False(t, b.nonstreamValidated)
	require.Error(t, b.nonstreamValidationErr)
	d.mu.Unlock()
}

func TestControlledResponseBodyEOFMarksTransportTerminalWithoutHealth(t *testing.T) {
	r := &ControlledRequest{Policy: scheduling.Policy{Enabled: true}}
	d := &controlledDispatch{request: r}
	b := &controlledResponseBody{dispatch: d, success: true, ReadCloser: io.NopCloser(strings.NewReader(`{"unexpected":true}`))}
	_, err := io.ReadAll(b)
	require.NoError(t, err)
	d.mu.Lock()
	require.True(t, d.transportTerminal)
	require.False(t, d.terminal)
	d.mu.Unlock()
}

func TestControlledErrorBodyDeadlineCancelsDispatch(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	r := &ControlledRequest{
		Policy: scheduling.Policy{Enabled: true},
		Ledger: scheduling.NewAttemptLedger(scheduling.DefaultRetryPolicy(), scheduling.LatencyProfile{TotalBudgetMS: 20}, time.Now(), time.Time{}),
	}
	d := &controlledDispatch{ctx: ctx, cancel: cancel, request: r}
	d.mu.Lock()
	d.startErrorBodyTimerLocked()
	d.mu.Unlock()
	select {
	case <-ctx.Done():
	case <-time.After(500 * time.Millisecond):
		t.Fatal("error-body deadline did not cancel dispatch")
	}
	d.mu.Lock()
	require.True(t, d.responseDrainTimeout)
	d.mu.Unlock()
	require.EqualError(t, d.responseReadError(context.Canceled), "upstream error body deadline exceeded")
	require.False(t, errors.Is(d.responseReadError(context.Canceled), context.Canceled))
}
