//go:build unit

package service

import (
	"context"
	"database/sql"
	"encoding/json"
	"github.com/liulixin-lex/xy2api/internal/scheduling"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
	"testing"
	"time"
)

func explainPGSnapshot(t *testing.T, db *sql.DB) map[string]string {
	t.Helper()
	out := map[string]string{}
	for _, table := range []string{"accounts", "scheduler_outbox", "scheduling_policies", "scheduling_controls", "scheduling_attempts", "scheduling_owner_sessions", "scheduling_session_grants"} {
		var rows string
		require.NoError(t, db.QueryRow("SELECT COALESCE(jsonb_agg(to_jsonb(t) ORDER BY to_jsonb(t)::text),'[]'::jsonb)::text FROM "+table+" t").Scan(&rows))
		out[table] = rows
	}
	return out
}
func explainRedisSnapshot(t *testing.T, r redis.UniversalClient) map[string]string {
	t.Helper()
	ctx := context.Background()
	keys, e := r.Keys(ctx, "*").Result()
	require.NoError(t, e)
	out := map[string]string{}
	for _, key := range keys {
		raw, e := r.Dump(ctx, key).Result()
		require.NoError(t, e)
		out[key] = raw
	}
	return out
}
func TestExplainRealPostgresRedisIsPureAndPredictsNextSelection(t *testing.T) {
	s, db, p, accounts := controlledIntegration(t, false)
	ctx := context.Background()
	group := &Group{ID: 7, Platform: PlatformOpenAI}
	for _, a := range accounts {
		a.Platform = PlatformOpenAI
		a.Type = AccountTypeOAuth
		// The synthetic fixture model is an explicit alias; OAuth defaults only
		// support real provider models, unlike unrestricted API-key fixtures.
		a.Credentials = map[string]any{"model_mapping": map[string]any{"test-model": "test-model"}}
	}
	_, fixtureErr := db.Exec(`UPDATE accounts SET credentials='{"model_mapping":{"test-model":"test-model"}}'::jsonb`)
	require.NoError(t, fixtureErr)
	gateway := &OpenAIGatewayService{accountRepo: s.accounts, schedulerSnapshot: &SchedulerSnapshotService{groupRepo: explainGroups{group: group}}}
	s.SetExplainEligibility(gateway.ExplainSchedulingEligibility, nil)
	s.concurrency = NewConcurrencyService(explainLoads{counts: map[int64]int{1: 2}})
	// An unknown ticket, admin family pause, genuine allocation reservation and
	// Redis expired data are all present before explanation. Nothing may clean them.
	ticket, e := s.Store.BeginDispatch(ctx, scheduling.DispatchRequest{AccountID: 1, RequestID: "explain-unknown", NodeID: "test", HardConcurrency: 10})
	require.NoError(t, e)
	require.NoError(t, s.Store.MarkAttemptUnknown(ctx, ticket.TicketID))
	_, e = s.Store.Control(ctx, scheduling.ControlCommand{AccountID: 3, Scope: scheduling.ScopeFamily, Action: "pause"})
	require.NoError(t, e)
	req := scheduling.SelectionRequest{Policy: p, Profile: scheduling.LatencyProfile{Name: "unconfigured"}, ContextBucket: "unknown", Transport: "http", Now: time.Now(), Candidates: []scheduling.Candidate{{AccountID: 1, Priority: 0, HardEligible: true, CapacityAvailable: true}, {AccountID: 2, Priority: 0, HardEligible: true, CapacityAvailable: true}, {AccountID: 3, Priority: 1, HardEligible: false, CapacityAvailable: true}}}
	initial, e := s.Runtime.Select(ctx, req)
	require.NoError(t, e)
	require.NoError(t, s.Runtime.CommitSelection(ctx, initial))
	pending, e := s.Runtime.Select(ctx, req)
	require.NoError(t, e)
	_ = pending
	require.NoError(t, s.redis.ZAdd(ctx, "concurrency:account:1", redis.Z{Score: 1, Member: "expired"}).Err())
	beforePG, beforeRedis := explainPGSnapshot(t, db), explainRedisSnapshot(t, s.redis)
	out, e := s.Explain(ctx, json.RawMessage("{\"group_id\":7,\"model\":\"test-model\",\"protocol\":\"http\"}"))
	require.NoError(t, e)
	require.Equal(t, beforePG, explainPGSnapshot(t, db), "policies, accounts, control, grants, tickets and outbox must be byte-equivalent")
	require.Equal(t, beforeRedis, explainRedisSnapshot(t, s.redis), "all score/reservation/probe/quota/lease values must remain byte-equivalent")
	result := out.(map[string]any)
	rows := result["candidates"].([]schedulingExplainRow)
	require.Equal(t, 2, rows[0].CurrentConcurrency)
	require.False(t, rows[2].Eligible)
	actual, e := s.Runtime.Select(ctx, req)
	require.NoError(t, e)
	require.Equal(t, actual.AccountID, *result["selected_account_id"].(*int64))
	require.NoError(t, s.Runtime.ReleaseSelection(ctx, actual))
}
