//go:build unit

package service

import (
	"context"
	"testing"
	"time"

	"github.com/liulixin-lex/xy2api/internal/scheduling"
	"github.com/stretchr/testify/require"
)

func TestControlledCandidateAdmissionFiltersBeforeSharedReads(t *testing.T) {
	for _, reason := range []string{"unsupported", "disabled_weight", "tried", "excluded", "admission_rejected", "different_owner"} {
		t.Run(reason, func(t *testing.T) {
			s, _, _, accounts := controlledIntegration(t, false)
			ctx, r := controlledIntegrationRequest(t, s)
			// This account deliberately has no database record. Every listed reason
			// makes it impossible to win, so its missing identity cannot fail preflight.
			unrelated := *accounts[0]
			unrelated.ID = 999
			pool := append(append([]*Account{}, accounts...), &unrelated)
			excluded := map[int64]struct{}{}
			allowed := func(a *Account) (bool, string) { return true, "test" }
			switch reason {
			case "unsupported":
				allowed = func(a *Account) (bool, string) { return a.ID != 999, "model_support" }
			case "disabled_weight":
				r.Policy.Accounts = append(r.Policy.Accounts, scheduling.AccountRule{AccountID: 999, Weight: 0})
			case "tried":
				r.ReplaySafe = true
				require.NoError(t, r.Ledger.BeginAttempt(999, 0, time.Now(), true))
			case "excluded":
				excluded[999] = struct{}{}
			case "admission_rejected":
				r.admissionRejected = map[int64]bool{999: true}
			case "different_owner":
				r.ownerAccountID = 1
			}
			result, err := s.selectAccount(ctx, r, pool, allowed, excluded, false)
			require.NoError(t, err)
			require.NotNil(t, result)
			require.NotEqualValues(t, 999, result.Account.ID)
			if reason == "different_owner" {
				require.EqualValues(t, 1, result.Account.ID)
			}
		})
	}
}

func TestControlledCandidateAdmissionBatchMatchesExistingDomains(t *testing.T) {
	s, _, _, accounts := controlledIntegration(t, false)
	ctx := context.Background()
	_, err := s.Store.PutFailureDomains(ctx, scheduling.AccountFailureDomains{AccountID: 1, QuotaPoolID: "group-quota", AvailabilityPoolID: "service-pool"}, 1)
	require.NoError(t, err)
	batch, err := s.candidateAdmissions(ctx, accounts, "test-model", "", true)
	require.NoError(t, err)
	individual, err := s.candidateAdmissions(ctx, accounts, "test-model", "", false)
	require.NoError(t, err)
	for _, account := range accounts {
		require.Equal(t, individual[account.ID], batch[account.ID], "account %d", account.ID)
	}
}
