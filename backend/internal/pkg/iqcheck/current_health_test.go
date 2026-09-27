package iqcheck

import (
	"encoding/json"
	"io"
	"net"
	"net/http"
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func healthResponse(body, media string, status int) Result {
	return ParseHTTP(&http.Response{StatusCode: status, Header: http.Header{"Content-Type": {media}}, Body: io.NopCloser(strings.NewReader(body))}, false, "http")
}

func healthTerminal(answer string) string {
	b, _ := json.Marshal(map[string]any{"type": "response.completed", "response": map[string]any{"id": "r_fixture", "status": "completed", "output": []any{map[string]any{"type": "message", "role": "assistant", "status": "completed", "content": []any{map[string]any{"type": "output_text", "text": answer}}}}}})
	return string(b)
}

func TestIQCompatibilityLongReasoning(t *testing.T) {
	for _, answer := range []string{`{"answer":21}`, `{"answer":29}`} {
		// Reproduce the observed >256 KiB cumulative stream shape with synthetic
		// reasoning. The production request did not retain its raw answer.
		event := "data: {\"type\":\"response.reasoning_text.delta\",\"delta\":\"" + strings.Repeat("x", 64000) + "\"}\n\n"
		body := strings.Repeat(event, 8) + "data: " + healthTerminal(answer) + "\n\ndata: [DONE]\n\n"
		r := healthResponse(body, "text/event-stream", 200)
		require.Equal(t, Grade(answer).Status, r.Status, r)
		require.Greater(t, r.Diagnostic.BytesRead, int64(256*1024))
		require.NotContains(t, r.Answer, "xxxxx")
	}
	// Large encrypted reasoning can also be repeated in the terminal object.
	body := strings.Replace(healthTerminal("21"), `"output":[`, `"output":[{"type":"reasoning","encrypted_content":"`+strings.Repeat("a", 400000)+`"},`, 1)
	require.Equal(t, "smart", healthResponse("data: "+body+"\n\n", "text/event-stream", 200).Status)
}

func TestIQErrorEnvelopeClassification(t *testing.T) {
	for _, tc := range []struct {
		body   string
		status int
		reason string
	}{
		{`{"error":{"message":"上游账户余额不足，请充值","type":"api_error"}}`, 403, "quota_exhausted"},
		{`{"error":"Insufficient balance"}`, 200, "quota_exhausted"},
		{`{"success":false,"code":400,"message":"余额不足"}`, 200, "quota_exhausted"},
		{`{"error":{"code":"insufficient_quota"}}`, 429, "quota_exhausted"},
		{`{"error":{"code":"rate_limit_exceeded"}}`, 429, "rate_limited"},
		{`{"error":{"message":"invalid API key"}}`, 401, "authentication_unavailable"},
		{`{"error":{"message":"upstream timed out"}}`, 504, "timeout"},
		{`{"error":{"code":"unknown","message":"unclassified fixture"}}`, 200, "upstream_error"},
		{`{"answer":21}`, 200, "incomplete_response"},
	} {
		r := healthResponse(tc.body, "application/json", tc.status)
		require.Equal(t, "unknown", r.Status, tc.body)
		require.Equal(t, tc.reason, r.Reason, tc.body)
		require.NotContains(t, string(r.Diagnostic.JSON()), "请充值")
		if tc.reason == "quota_exhausted" {
			require.False(t, Retryable(r))
		}
	}
	require.Equal(t, "upstream_html_response", healthResponse("<html>gateway</html>", "text/html", 200).Reason)
}

func TestIQTerminalCompatibilityPreservesEvidence(t *testing.T) {
	good := healthTerminal("21")
	for _, body := range []string{"event: message\ndata: " + good + "\n\n", "event: response.completed\ndata: " + strings.Replace(good, `"type":"response.completed",`, "", 1) + "\n\n"} {
		require.Equal(t, "smart", healthResponse(body, "text/event-stream", 200).Status)
	}
	for _, body := range []string{
		"data: [DONE]\n\n",
		"data: " + strings.Replace(good, `"status":"completed"`, `"status":"incomplete"`, 1) + "\n\n",
		"data: " + good + "\n\ndata: {\"error\":{\"code\":\"insufficient_quota\"}}\n\n",
		"event: response.failed\ndata: " + good + "\n\n",
	} {
		require.Equal(t, "unknown", healthResponse(body, "text/event-stream", 200).Status, body)
	}
}

func TestIQLimitDiagnostics(t *testing.T) {
	body := strings.Repeat(" ", MaxResponseBytes) + "{}"
	r := Parse(strings.NewReader(body), false, false)
	require.Equal(t, "response_too_large", r.Reason)
	require.Equal(t, "response_bytes", r.Diagnostic.LimitKind)
	require.Equal(t, MaxResponseBytes, r.Diagnostic.LimitBytes)
	r = healthResponse("data: "+strings.Repeat("x", MaxEventBytes+1)+"\n\n", "text/event-stream", 200)
	require.Equal(t, "event_too_large", r.Reason)
	require.Equal(t, "event_bytes", r.Diagnostic.LimitKind)
	r = healthResponse("data: "+healthTerminal(strings.Repeat("x", MaxAnswerBytes+1))+"\n\n", "text/event-stream", 200)
	require.Equal(t, "answer_too_large", r.Reason)
	require.Equal(t, "answer_bytes", r.Diagnostic.LimitKind)
}

func TestIQSocketTimeoutClassification(t *testing.T) {
	err := &net.OpError{Op: "read", Net: "tcp", Err: os.ErrDeadlineExceeded}
	for _, body := range []io.Reader{brokenReader{err}, io.MultiReader(strings.NewReader("data: "+healthTerminal("21")+"\n\n"), brokenReader{err})} {
		r := ParseHTTP(&http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"text/event-stream"}}, Body: io.NopCloser(body)}, false, "http")
		require.Equal(t, "unknown", r.Status)
		require.Equal(t, "timeout", r.Reason)
		require.Equal(t, "timeout", r.Diagnostic.Category)
	}
}
