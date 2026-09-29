//go:build unit

package service

import (
	"net/http"
	"testing"

	"github.com/liulixin-lex/xy2api/internal/scheduling"
	"github.com/stretchr/testify/require"
)

func TestControlledBufferedFailureParityRealSSE(t *testing.T) {
	for _, route := range []string{"chat", "messages", "responses", "passthrough", "grok", "grokchat"} {
		for _, tc := range []struct {
			name, body                          string
			attributable, wantError, replaySafe bool
		}{
			{"bare_server_error", `{"type":"error","error":{"type":"server_error","code":"server_error","message":"Internal server error"}}`, true, true, true},
			{"context_length", `{"type":"response.failed","response":{"status":"failed","output":[],"error":{"type":"invalid_request_error","code":"context_length_exceeded","message":"context length exceeded"}}}`, false, true, false},
			{"bare_content_policy", `{"type":"error","error":{"code":"content_policy_violation","message":"Content policy violation"}}`, false, true, false},
			{"incomplete", `{"type":"response.incomplete","response":{"id":"resp_1","status":"incomplete","output":[],"incomplete_details":{"reason":"max_output_tokens"},"usage":{"input_tokens":1,"output_tokens":1}}}`, false, false, true},
		} {
			t.Run(route+"/"+tc.name, func(t *testing.T) {
				request := runControlledBridgeRealSSE(t, route, "data: "+tc.body+"\n\n", false, controlledBridgeFailureExpectation{wantError: tc.wantError, attributable: tc.attributable})
				request.mu.Lock()
				replaySafe := request.ReplaySafe
				request.mu.Unlock()
				require.Equal(t, tc.replaySafe, replaySafe, "request-specific rejection must not dispatch another account")
			})
		}
	}
}

func TestControlledBufferedNativeFailureParityRealSSE(t *testing.T) {
	for _, route := range []string{"nativechat", "nativeresponses", "gatewaychat", "gatewayresponses"} {
		for _, tc := range []struct {
			name, detail string
			provider     bool
		}{
			{"provider", `{"type":"overloaded_error","message":"busy"}`, true},
			{"request_type_without_code", `{"type":"invalid_request_error","message":"invalid request"}`, false},
		} {
			t.Run(route+"/"+tc.name, func(t *testing.T) {
				body := controlledAnthropicStart + controlledAnthropicDelta + "event: error\ndata: {\"type\":\"error\",\"error\":" + tc.detail + "}\n\n" + controlledAnthropicStop
				request := runControlledBridgeRealSSE(t, route, body, false, controlledBridgeFailureExpectation{wantError: true, attributable: tc.provider})
				request.mu.Lock()
				replaySafe := request.ReplaySafe
				request.mu.Unlock()
				require.Equal(t, tc.provider, replaySafe)
			})
		}
	}
}

func TestControlledBufferedFailureObservationHasNoLatencyEvidence(t *testing.T) {
	for _, tc := range []struct {
		name, body                     string
		provider, excluded, replaySafe bool
	}{
		{"bare_server_error", `{"type":"error","error":{"code":"server_error"}}`, true, false, true},
		{"failed_terminal", `{"type":"response.failed","response":{"status":"failed","error":{"type":"server_error"}}}`, true, false, true},
		{"failed_completed_envelope", `{"type":"response.completed","response":{"status":"failed","output":[]}}`, true, false, true},
		{"invalid_request_type_only", `{"type":"error","error":{"type":"invalid_request_error"}}`, false, true, false},
		{"request_code", `{"type":"error","error":{"code":"invalid_request"}}`, false, true, false},
		{"context_length", `{"type":"response.failed","response":{"error":{"code":"context_length_exceeded"}}}`, false, true, false},
		{"policy", `{"type":"error","error":{"code":"content_policy_violation"}}`, false, true, false},
		{"cyber_policy", `{"type":"response.failed","response":{"error":{"code":"cyber_policy"}}}`, false, true, false},
		{"incomplete", `{"type":"response.incomplete","response":{"incomplete_details":{"reason":"max_output_tokens"}}}`, false, true, true},
		{"cancelled", `{"type":"response.cancelled"}`, false, true, true},
		{"semantic_delta_ignored", `{"type":"response.output_text.delta","delta":"hello"}`, false, false, true},
		{"success_ignored", `{"type":"response.completed","response":{"status":"completed","output":[]}}`, false, false, true},
		{"malformed_ignored", `{"type":"error"`, false, false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := &ControlledRequest{Policy: scheduling.Policy{Enabled: true}, ReplaySafe: true}
			d := &controlledDispatch{request: r}
			b := &controlledResponseBody{dispatch: d, success: true, sse: true, buffered: true}
			resp := &http.Response{StatusCode: http.StatusOK, Body: b}
			observeControlledBufferedFailure(resp, []byte(tc.body))
			require.Equal(t, tc.provider, d.upstreamFailure)
			require.Equal(t, tc.excluded, d.excluded)
			require.Equal(t, tc.replaySafe, r.ReplaySafe)
			require.True(t, d.semantic.IsZero())
			require.True(t, d.answer.IsZero())
			require.True(t, d.firstEvent.IsZero())
			require.False(t, d.transportTerminal, "failure observation alone does not settle the adapter")
			require.False(t, b.nonstreamValidated)
		})
	}
}

func TestControlledBufferedFailureObservationOwnership(t *testing.T) {
	for _, tc := range []struct {
		name              string
		enabled, buffered bool
	}{
		{"disabled", false, true},
		{"streaming", true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := &ControlledRequest{Policy: scheduling.Policy{Enabled: tc.enabled}, ReplaySafe: true}
			d := &controlledDispatch{request: r}
			resp := &http.Response{Body: &controlledResponseBody{dispatch: d, success: true, buffered: tc.buffered}}
			observeControlledBufferedFailure(resp, []byte(`{"type":"error","error":{"code":"context_length_exceeded"}}`))
			require.True(t, r.ReplaySafe)
			require.False(t, d.upstreamFailure)
			require.False(t, d.excluded)
		})
	}
}
