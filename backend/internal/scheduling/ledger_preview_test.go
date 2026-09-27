package scheduling

import (
	"github.com/stretchr/testify/require"
	"testing"
	"time"
)

func TestFallbackCurrentLedgerCounterexamples(t *testing.T) {
	for _, name := range []string{"same_tier", "total_attempts", "after_timeout", "no_after_timeout"} {
		t.Run(name, func(t *testing.T) {
			now := time.Now()
			policy := DefaultRetryPolicy()
			p := profile()
			if name == "total_attempts" {
				policy.MaxAttempts = 2
			}
			if name == "no_after_timeout" {
				policy.MaxAfterTimeout = 0
			}
			ledger := NewAttemptLedger(policy, p, now, time.Time{})
			if name != "no_after_timeout" {
				require.NoError(t, ledger.BeginAttempt(1, 0, now, true))
			}
			if name == "after_timeout" {
				ledger.MarkFirstOutputTimeout()
			}
			nextTier := 1
			if name == "same_tier" {
				nextTier = 0
			}
			before := ledger.CanAttempt(3, nextTier, now, true)
			require.NoError(t, before)
			require.NoError(t, ledger.BeginAttempt(2, 0, now, true))
			ledger.MarkFirstOutputTimeout()
			after := ledger.CanAttempt(3, nextTier, now, true)
			require.ErrorIs(t, after, ErrAttemptBudget)
			t.Logf("BASELINE pre-dispatch fallback allowed=%v; after pending dispatch+timeout allowed=%v error=%v", before == nil, after == nil, after)
		})
	}
}

func TestLedgerPendingPreviewPreservesCountsAndPriority(t *testing.T) {
	now := time.Now()
	p := DefaultRetryPolicy()
	l := NewAttemptLedger(p, profile(), now, time.Time{})
	require.NoError(t, l.BeginAttempt(1, 0, now, true))
	before := l.Snapshot()
	require.ErrorIs(t, l.CanAttemptAfterDispatch(2, 0, 3, 0, now, true), ErrAttemptBudget)
	require.ErrorIs(t, l.CanAttemptAfterDispatch(2, 1, 3, 0, now, true), ErrAttemptBudget, "never ascend after pending descent")
	require.NoError(t, l.CanAttemptAfterDispatch(2, 0, 3, 1, now, true))
	require.Equal(t, before, l.Snapshot())
	p.CrossTier = false
	l = NewAttemptLedger(p, profile(), now, time.Time{})
	require.ErrorIs(t, l.CanAttemptAfterDispatch(1, 0, 2, 1, now, true), ErrAttemptBudget)
	p.MaxAfterTimeout = 0
	l = NewAttemptLedger(p, profile(), now, time.Time{})
	require.ErrorIs(t, l.CanAttemptAfterDispatch(1, 0, 2, 0, now, true), ErrAttemptBudget)
}
