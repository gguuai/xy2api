package iqcheck

import (
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestIQCompletedItemsRecovery(t *testing.T) {
	done := `data: {"type":"response.output_item.done","output_index":1,"item":{"id":"msg_1","type":"message","role":"assistant","phase":"final_answer","status":"completed","content":[{"type":"output_text","text":"21"}]}}` + "\n\n"
	terminal := `data: {"type":"response.completed","response":{"id":"resp_1","status":"completed","output":[]}}` + "\n\n"
	for _, tc := range []struct{ name, body, status, reason string }{
		{"empty terminal", done + terminal, "smart", "correct_answer"},
		{"idempotent done", done + done + terminal, "smart", "correct_answer"},
		{"wrong answer", strings.Replace(done, `"text":"21"`, `"text":"29"`, 1) + terminal, "degraded", "wrong_answer"},
		{"missing completion", done, "unknown", "incomplete_response"},
		{"delta only", `data: {"type":"response.output_text.delta","delta":"21"}` + "\n\n" + terminal, "unknown", "missing_final_message"},
		{"conflicting done", done + strings.Replace(done, `"text":"21"`, `"text":"29"`, 1) + terminal, "unknown", "conflicting_final_response"},
		{"conflicting index", done + strings.Replace(done, `"output_index":1`, `"output_index":2`, 1) + terminal, "unknown", "conflicting_output_identity"},
		{"commentary", strings.Replace(done, `"final_answer"`, `"commentary"`, 1) + terminal, "unknown", "missing_final_message"},
		{"empty final", strings.Replace(done, `"text":"21"`, `"text":""`, 1) + terminal, "unknown", "empty_final_text"},
		{"incomplete item", strings.Replace(done, `"status":"completed"`, `"status":"in_progress"`, 1) + terminal, "unknown", "invalid_completed_message"},
		{"missing item status", strings.Replace(done, `"status":"completed",`, "", 1) + terminal, "unknown", "invalid_completed_message"},
		{"user item", strings.Replace(done, `"role":"assistant"`, `"role":"user"`, 1) + terminal, "unknown", "invalid_completed_message"},
		{"failed", done + strings.Replace(terminal, `"status":"completed"`, `"status":"failed"`, 1), "unknown", "incomplete_response"},
		{"refusal", strings.Replace(done, `"output_text"`, `"refusal"`, 1) + terminal, "unknown", "refusal"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := ParseHTTP(&http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(tc.body))}, false, "http")
			require.Equal(t, tc.status, r.Status)
			require.Equal(t, tc.reason, r.Reason)
			if tc.name == "commentary" {
				require.Equal(t, "commentary", r.Diagnostic.IgnoredTypes)
			}
			if r.Status == "smart" {
				require.Equal(t, "completed_items", r.Diagnostic.AnswerSource)
				require.Equal(t, 1, r.Diagnostic.DoneMessages)
			}
		})
	}
}

func TestIQRecoveryRetryPolicy(t *testing.T) {
	for _, reason := range []string{"timeout", "request_failed", "response_read_failed", "http_502", "http_503", "http_504", "rate_limited", "missing_final_message", "empty_final_text", "incomplete_response", "zero_byte_response"} {
		require.True(t, Retryable(Unknown(reason)), reason)
	}
	for _, reason := range []string{"http_403", "quota_exhausted", "wrong_answer", "refusal"} {
		require.False(t, Retryable(Unknown(reason)), reason)
	}
	require.False(t, Retryable(Grade("29")))
	r := Unknown("timeout")
	r.Diagnostic = &Diagnostic{Transport: "plugin"}
	require.False(t, Retryable(r))
}

func TestIQCompletedItemIdentityAndPartialTerminal(t *testing.T) {
	item := `{"id":"msg_1","type":"message","role":"assistant","phase":"final_answer","status":"completed","content":[{"type":"output_text","text":"21"}]}`
	done := `data: {"type":"response.output_item.done","output_index":1,"item":` + item + "}\n\n"
	for _, tc := range []struct{ name, output, status string }{
		{"same envelope", item, "smart"},
		{"missing item with reasoning", `{"id":"reason_1","type":"reasoning"}`, "smart"},
		{"conflicting role", strings.Replace(item, `"assistant"`, `"user"`, 1), "unknown"},
		{"conflicting phase", strings.Replace(item, `"final_answer"`, `"commentary"`, 1), "unknown"},
		{"conflicting type", strings.Replace(item, `"message"`, `"reasoning"`, 1), "unknown"},
		{"conflicting value", strings.Replace(item, `"21"`, `"29"`, 1), "unknown"},
		{"independent final", strings.Replace(item, `"msg_1"`, `"msg_2"`, 1), "unknown"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := done + `data: {"type":"response.completed","response":{"status":"completed","output":[` + tc.output + "]}}\n\n"
			r := Parse(strings.NewReader(body), true, false)
			require.Equal(t, tc.status, r.Status, r.Reason)
		})
	}
	for _, body := range []string{strings.Replace(done, `"output_index":1`, `"output_index":1,"OUTPUT_INDEX":2`, 1), strings.Replace(done, `"id":"msg_1",`, "", 1)} {
		r := Parse(strings.NewReader(body+`data: {"type":"response.completed","response":{"status":"completed","output":[]}}`+"\n\n"), true, false)
		require.Equal(t, "unknown", r.Status)
	}
}
