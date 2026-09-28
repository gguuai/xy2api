//go:build unit

package service

import (
	"github.com/liulixin-lex/xy2api/internal/scheduling"
	"github.com/stretchr/testify/require"
	"testing"
)

func TestAuditControlledStreamingTerminalVerdict(t *testing.T) {
	for _, tc := range []struct {
		name, frame                string
		failed, excluded, terminal bool
	}{
		{"single_l_canceled", `{"type":"response.canceled"}`, false, true, true},
		{"done_failed_status", `{"type":"response.done","response":{"status":"failed","output":[]}}`, true, false, true},
		{"done_incomplete_status", `{"type":"response.done","response":{"status":"incomplete","output":[]}}`, false, true, true},
		{"completed_error_object", `{"type":"response.completed","response":{"status":"failed","error":{"type":"server_error"}}}`, true, false, true},
		{"completed_success", `{"type":"response.completed","response":{"status":"completed","output":[]}}`, false, false, true},
		{"truncated_error_frame", `{"type":"error","error":{"type":"server_error"}`, false, false, false},
		{"done_sentinel", `[DONE]`, false, false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := &ControlledRequest{Policy: scheduling.Policy{Enabled: true}, ReplaySafe: true}
			d := &controlledDispatch{request: r, semanticReady: make(chan struct{})}
			d.ObserveFrame([]byte(tc.frame))
			t.Logf("OBSERVED upstream_failure=%v excluded=%v terminal=%v semantic=%v", d.upstreamFailure, d.excluded, d.terminal, !d.semantic.IsZero())
			require.Equal(t, tc.failed, d.upstreamFailure)
			require.Equal(t, tc.excluded, d.excluded)
			require.Equal(t, tc.terminal, d.terminal)
			require.True(t, d.semantic.IsZero())
		})
	}
}
