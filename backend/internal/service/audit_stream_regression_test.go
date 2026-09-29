//go:build unit

package service

import (
	"github.com/liulixin-lex/xy2api/internal/scheduling"
	"github.com/stretchr/testify/require"
	"testing"
	"time"
)

func TestAuditControlledStreamingRequestErrorType(t *testing.T) {
	for _, tc := range []struct {
		name, frame          string
		excluded, replaySafe bool
	}{
		{"anthropic_type_only", `{"type":"error","error":{"type":"invalid_request_error","message":"invalid request"}}`, true, false},
		{"responses_type_only", `{"type":"response.failed","response":{"error":{"type":"invalid_request_error"}}}`, true, false},
		{"cyber_policy", `{"type":"response.failed","response":{"error":{"code":"cyber_policy"}}}`, true, false},
		{"provider_overloaded", `{"type":"error","error":{"type":"overloaded_error"}}`, false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := &ControlledRequest{Policy: scheduling.Policy{Enabled: true}, ReplaySafe: true}
			d := &controlledDispatch{request: r, semanticReady: make(chan struct{})}
			d.ObserveFrame([]byte(tc.frame))
			t.Logf("OBSERVED upstream_failure=%v excluded=%v replay_safe=%v terminal=%v semantic=%v", d.upstreamFailure, d.excluded, r.ReplaySafe, d.terminal, !d.semantic.IsZero())
			require.Equal(t, tc.excluded, d.excluded)
			require.Equal(t, !tc.excluded, d.upstreamFailure)
			require.Equal(t, tc.replaySafe, r.ReplaySafe)
			require.True(t, d.terminal)
			require.True(t, d.semantic.IsZero())
		})
	}
}

func TestControlledStreamingRequestErrorPreservesSemanticCommit(t *testing.T) {
	now := time.Now()
	r := &ControlledRequest{Policy: scheduling.Policy{Enabled: true}, ReplaySafe: true, Started: now, Ledger: scheduling.NewAttemptLedger(scheduling.DefaultRetryPolicy(), scheduling.LatencyProfile{}, now, time.Time{})}
	d := &controlledDispatch{request: r, semanticReady: make(chan struct{})}
	frame := []byte(`{"type":"response.output_text.delta","delta":"hello"}`)
	d.ObserveFrame(frame)
	first := d.semantic
	require.False(t, first.IsZero())
	require.False(t, r.Ledger.Snapshot().Committed, "upstream observation is not client commit")
	d.CommitOutput(frame)
	committed := r.semanticAt
	require.True(t, r.Ledger.Snapshot().Committed)
	d.ObserveFrame([]byte(`{"type":"error","error":{"type":"invalid_request_error"}}`))
	require.Equal(t, first, d.semantic)
	require.Equal(t, committed, r.semanticAt)
	require.True(t, r.Ledger.Snapshot().Committed)
	require.True(t, d.excluded)
	require.False(t, r.ReplaySafe)
}
