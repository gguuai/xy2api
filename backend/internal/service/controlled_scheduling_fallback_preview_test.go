//go:build unit

package service

import (
	"context"
	"github.com/liulixin-lex/xy2api/internal/scheduling"
	"github.com/stretchr/testify/require"
	"testing"
	"time"
)

func TestControlledFallbackUsesRemainingAttemptBudget(t *testing.T) {
	cases := []struct {
		name          string
		prior         bool
		timeout       bool
		maxAttempts   int
		thirdPriority int
		want          bool
	}{
		{"same_tier_third_remains_legal", true, false, 3, 0, true},
		{"total_attempt_limit_includes_pending", true, false, 2, 1, false},
		{"timeout_uses_remaining_total_attempts", true, true, 3, 1, true},
		{"first_attempt_can_leave_a_fallback", false, false, 3, 1, true},
		{"legal_lower_tier_remains_available", true, false, 3, 1, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s, _, _, accounts := controlledIntegration(t, false)
			ctx := context.Background()
			accounts[2].Priority = tc.thirdPriority
			rec, e := s.Store.GetGroupPolicy(ctx, 7)
			require.NoError(t, e)
			rec.Policy.MaxAttempts = tc.maxAttempts
			for i := range rec.Policy.Accounts {
				if rec.Policy.Accounts[i].AccountID == accounts[2].ID {
					rec.Policy.Accounts[i].Priority = &tc.thirdPriority
				}
			}
			_, e = s.Store.PutGroupPolicy(ctx, rec.Policy, rec.Version)
			require.NoError(t, e)
			request, r := controlledIntegrationRequest(t, s)
			if tc.prior {
				require.NoError(t, r.Ledger.BeginAttempt(1, 0, time.Now(), true))
			}
			if tc.timeout {
				r.Ledger.MarkFirstOutputTimeout()
			}
			before := r.Ledger.Snapshot()
			selected, e := s.selectAccount(request, r, accounts, func(a *Account) (bool, string) { return true, "test" }, nil, false)
			require.NoError(t, e)
			r.mu.Lock()
			fallback := r.fallback
			decision := r.Decision
			r.mu.Unlock()
			require.False(t, r.Policy.Retry.ReserveFallback, "account pools do not reserve an old fallback time window")
			require.False(t, fallback)
			require.Equal(t, before, r.Ledger.Snapshot(), "selection must not consume an actual attempt")
			// Advance the actual request ledger as dispatch does. A first-output timeout
			// consumes time/attempts, but it does not introduce a separate retry cap.
			require.NoError(t, r.Ledger.BeginAttempt(selected.Account.ID, decision.Priority, time.Now(), true))
			r.Ledger.MarkFirstOutputTimeout()
			dispatched := r.Ledger.Snapshot()
			require.Equal(t, before.Attempts+1, dispatched.Attempts)
			next, nextErr := s.selectAccount(request, r, accounts, func(a *Account) (bool, string) { return true, "test" }, nil, false)
			if !tc.want {
				require.ErrorIs(t, nextErr, scheduling.ErrAttemptBudget)
				require.Nil(t, next)
			} else {
				require.NoError(t, nextErr)
				require.NotNil(t, next)
				require.NotEqual(t, selected.Account.ID, next.Account.ID, "the next attempt must use an untried account")
				expectedNext := int64(2)
				if tc.prior {
					expectedNext = 3
				}
				require.Equal(t, expectedNext, next.Account.ID)
				require.NoError(t, r.Ledger.CanAttempt(next.Account.ID, r.Decision.Priority, time.Now(), true))
			}
			require.Equal(t, dispatched, r.Ledger.Snapshot(), "finding the next candidate must not spend its attempt")

		})
	}
}
