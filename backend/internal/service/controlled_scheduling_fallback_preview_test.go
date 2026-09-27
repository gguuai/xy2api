//go:build unit

package service

import (
	"context"
	"github.com/stretchr/testify/require"
	"testing"
	"time"
)

func TestControlledFallbackPreviewsPendingAttemptLimits(t *testing.T) {
	cases := []struct {
		name          string
		prior         bool
		timeout       bool
		maxAttempts   int
		afterTimeout  int
		thirdPriority int
		exhaustCredit bool
		want          bool
	}{
		{"same_tier_third_is_illegal", true, false, 3, 1, 0, false, false},
		{"total_attempt_limit_includes_pending", true, false, 2, 1, 1, false, false},
		{"timeout_followup_limit_includes_pending", true, true, 3, 1, 1, false, false},
		{"first_timeout_has_no_followup", false, false, 3, 0, 1, false, false},
		{"current_retry_spends_last_shared_token", true, false, 3, 1, 1, true, false},
		{"legal_lower_tier_remains_available", true, false, 3, 1, 1, false, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s, _, p, accounts := controlledIntegration(t, true)
			ctx := context.Background()
			accounts[2].Priority = tc.thirdPriority
			p.Retry.MaxAttempts = tc.maxAttempts
			p.Retry.MaxAfterTimeout = tc.afterTimeout
			rec, e := s.Store.PutPolicy(ctx, p, p.Version)
			require.NoError(t, e)
			p = *rec.Policy
			request, r := controlledIntegrationRequest(t, s)
			if tc.prior {
				require.NoError(t, r.Ledger.BeginAttempt(1, 0, time.Now(), true))
			}
			if tc.timeout {
				r.Ledger.MarkFirstOutputTimeout()
			}
			if tc.exhaustCredit {
				_, e = s.Runtime.AcquireDispatchBudget(ctx, p, "other-logical-request", true)
				require.NoError(t, e)
			}
			before := r.Ledger.Snapshot()
			selected, e := s.selectAccount(request, r, accounts, func(a *Account) (bool, string) { return true, "test" }, nil, false)
			require.NoError(t, e)
			r.mu.Lock()
			fallback := r.fallback
			decision := r.Decision
			r.mu.Unlock()
			require.Equal(t, tc.want, fallback, "selected account %d must not reserve an impossible next attempt", selected.Account.ID)
			require.Equal(t, before, r.Ledger.Snapshot(), "preview must not consume a real attempt")
			s.releaseDecision(decision)
		})
	}
}
