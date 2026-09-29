//go:build unit

package service

import (
	"context"
	"github.com/liulixin-lex/xy2api/internal/scheduling"
	"github.com/stretchr/testify/require"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestSchedulingModeAuxiliaryUsesGroupRulesWithoutGenerationReservations(t *testing.T) {
	s, db, _, accounts := controlledIntegration(t, false)
	ctx, r := controlledIntegrationRequest(t, s)
	r.Auxiliary = true
	_, err := db.ExecContext(ctx, "UPDATE accounts SET schedulable=FALSE WHERE id=1")
	require.NoError(t, err)
	policy, err := s.Store.ReadGroupPolicy(ctx, 7)
	require.NoError(t, err)
	for i := range policy.Accounts {
		if policy.Accounts[i].AccountID == 2 {
			policy.Accounts[i].Weight = 0
		}
	}
	rec, err := s.Store.PutGroupPolicy(ctx, policy, policy.Version)
	require.NoError(t, err)
	r.Policy, r.Profile = accountPoolPolicy(rec.Policy, "test-model")
	pick, err := s.selectAccount(ctx, r, accounts, func(*Account) (bool, string) { return true, "eligible" }, nil, true)
	require.NoError(t, err)
	require.EqualValues(t, 3, pick.Account.ID)
	require.False(t, pick.Acquired)
	require.Nil(t, pick.ReleaseFunc)
	require.False(t, r.decisionPending)
	require.Empty(t, r.Decision.ProbeToken)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://fixture/v1/messages/count_tokens", strings.NewReader("{}"))
	require.NoError(t, err)
	calls := 0
	resp, err := s.roundTrip(req, 3, 1, func(*http.Request) (*http.Response, error) {
		calls++
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader("{}")), Header: http.Header{}}, nil
	})
	require.NoError(t, err)
	require.NoError(t, resp.Body.Close())
	require.Equal(t, 1, calls)
	require.Zero(t, r.Ledger.Snapshot().Attempts)
	var attempts int
	require.NoError(t, db.QueryRow("SELECT COUNT(*) FROM scheduling_attempts").Scan(&attempts))
	require.Zero(t, attempts)
	_, err = db.ExecContext(ctx, "UPDATE accounts SET schedulable=FALSE WHERE id=3")
	require.NoError(t, err)
	_, err = s.roundTrip(req, 3, 1, func(*http.Request) (*http.Response, error) { calls++; return nil, nil })
	require.ErrorIs(t, err, scheduling.ErrControlBlocked)
	require.Equal(t, 1, calls)
}
func TestSchedulingModeSub2APIBypassesControlledDispatchStore(t *testing.T) {
	s, db, _, _ := controlledIntegration(t, false)
	ctx := NewControlledRequestContext(context.Background(), "responses")
	_, err := s.Store.Control(context.Background(), scheduling.ControlCommand{AccountID: 1, Action: "pause"})
	require.NoError(t, err)
	_, err = s.Store.PutSchedulingMode(context.Background(), scheduling.ModeSub2API, 0)
	require.NoError(t, err)
	_, enabled, err := s.loadPolicy(ctx, nil, "m", "")
	require.NoError(t, err)
	require.False(t, enabled)
	require.NoError(t, db.Close())
	calls := 0
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://fixture/v1/responses", strings.NewReader("{}"))
	require.NoError(t, err)
	resp, err := s.roundTrip(req, 1, 1, func(*http.Request) (*http.Response, error) {
		calls++
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader("{}"))}, nil
	})
	require.NoError(t, err)
	require.NoError(t, resp.Body.Close())
	require.Equal(t, 1, calls)
}
