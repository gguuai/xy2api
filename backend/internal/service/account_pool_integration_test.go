//go:build unit

package service

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/liulixin-lex/xy2api/internal/scheduling"
	"github.com/stretchr/testify/require"
)

func TestAccountPoolRealDispatchExhaustsEligibleTier(t *testing.T) {
	s, db, _, accounts := controlledIntegration(t, false)
	_, err := db.Exec("INSERT INTO accounts(id,priority) VALUES(4,0),(5,0); INSERT INTO account_groups(account_id,group_id) VALUES(4,7),(5,7)")
	require.NoError(t, err)
	for _, id := range []int64{4, 5} {
		accounts = append(accounts, &Account{ID: id, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Priority: 0, Concurrency: 10, Status: StatusActive, Schedulable: true, GroupIDs: []int64{7}})
	}
	s.accounts.(*controlledIntegrationRepo).accounts = accounts
	policy := scheduling.DefaultGroupPolicy(7)
	policy.MaxAttempts = 5
	_, err = s.Store.PutGroupPolicy(context.Background(), policy, 1)
	require.NoError(t, err)
	ctx, r := controlledIntegrationRequest(t, s)
	var ids []int64
	eligible := func(a *Account) (bool, string) { return a.ID != 1, "requested_model_not_supported" }
	for i := 0; i < 4; i++ {
		pick, err := s.selectAccount(ctx, r, accounts, eligible, nil, false)
		require.NoError(t, err)
		id := pick.Account.ID
		ids = append(ids, id)
		response, err := controlledHTTP(t, s, ctx, id, func(w http.ResponseWriter, _ *http.Request) {
			if id != 3 {
				w.WriteHeader(http.StatusServiceUnavailable)
				fmt.Fprint(w, "fixture failure")
				return
			}
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, "{\"ok\":true}")
		})
		require.NoError(t, err)
		controlledConsume(t, response)
	}
	require.Equal(t, []int64{2, 4, 5, 3}, ids, "all three eligible peers must be sent before the lower tier; unsupported account 1 must never be sent")
	require.Equal(t, 4, r.Ledger.Snapshot().Attempts)
	var sent int
	require.NoError(t, db.QueryRow("SELECT COUNT(*) FROM scheduling_attempts WHERE request_id=$1 AND metrics ? 'sent_at'", r.ID).Scan(&sent))
	require.Equal(t, 4, sent)
	t.Logf("ACTUAL_DISPATCH order=%v unsupported=1 attempts=%d lower_tier_only_after_three_peers=true", ids, sent)
}

func TestAccountPoolCancelsAttemptBeforeNextTransport(t *testing.T) {
	s, _, _, accounts := controlledIntegration(t, true)
	ctx, r := controlledIntegrationRequest(t, s)
	var active, maxActive, cancelled atomic.Int32
	transport := func(req *http.Request) (*http.Response, error) {
		current := active.Add(1)
		defer active.Add(-1)
		for {
			old := maxActive.Load()
			if current <= old || maxActive.CompareAndSwap(old, current) {
				break
			}
		}
		if cancelled.Load() == 0 {
			<-req.Context().Done()
			cancelled.Add(1)
			return nil, req.Context().Err()
		}
		return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader("{\"ok\":true}"))}, nil
	}
	first := controlledPick(t, s, ctx, r, accounts)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://fixture", strings.NewReader("{}"))
	require.NoError(t, err)
	_, err = s.roundTrip(req, first.ID, 10, transport)
	require.Error(t, err)
	require.EqualValues(t, 1, cancelled.Load())
	require.Zero(t, active.Load())
	require.Len(t, r.history, 1)
	require.Equal(t, "attempt_timeout", r.history[0].Outcome)
	require.Nil(t, r.history[0].FirstSemanticMS)
	require.False(t, r.Ledger.Snapshot().TimeoutSeen, "non-streaming timeout must not invent semantic timing evidence")
	identity, err := s.controlledHealthIdentity(ctx, first)
	require.NoError(t, err)
	raw, err := s.redis.HGet(ctx, scheduling.HealthRedisKey(first.ID, r.Policy.Model, r.Profile, r.Reasoning, controlledBucket(r), r.Protocol, identity), "snapshot").Bytes()
	require.NoError(t, err)
	var health scheduling.HealthSnapshot
	require.NoError(t, json.Unmarshal(raw, &health))
	require.Equal(t, scheduling.HealthOpen, health.State)
	require.Len(t, health.Samples, 1)
	require.True(t, health.Samples[0].AttributableFailure)
	require.False(t, health.Samples[0].HasSemanticOutput)
	require.False(t, health.Samples[0].Timeout)
	require.Zero(t, health.Samples[0].TTFTMS)
	second := controlledPick(t, s, ctx, r, accounts)
	require.NotEqual(t, first.ID, second.ID)
	req, err = http.NewRequestWithContext(ctx, http.MethodPost, "http://fixture", strings.NewReader("{}"))
	require.NoError(t, err)
	response, err := s.roundTrip(req, second.ID, 10, transport)
	require.NoError(t, err)
	controlledConsume(t, response)
	require.EqualValues(t, 1, maxActive.Load())
	require.Equal(t, 2, r.Ledger.Snapshot().Attempts)
	t.Logf("CANCEL_BEFORE_SWITCH first=%d second=%d cancelled=%d max_active_transport=%d", first.ID, second.ID, cancelled.Load(), maxActive.Load())
}
