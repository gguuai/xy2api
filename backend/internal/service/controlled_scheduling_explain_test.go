package service

import (
	"context"
	"database/sql"
	"encoding/json"
	"github.com/DATA-DOG/go-sqlmock"
	"github.com/alicebob/miniredis/v2"
	"github.com/liulixin-lex/xy2api/internal/scheduling"
	"github.com/redis/go-redis/v9" //nolint:depguard // isolated Redis fixture for the read-only explain test.
	"github.com/stretchr/testify/require"
	"testing"
	"time"
)

type explainAccounts struct {
	AccountRepository
	accounts []Account
}

func (r explainAccounts) ListByGroup(context.Context, int64) ([]Account, error) {
	return r.accounts, nil
}
func (r explainAccounts) ListAllWithFilters(context.Context, string, string, string, string, int64, string) ([]Account, error) {
	return r.accounts, nil
}

type explainLoads struct {
	ConcurrencyCache
	counts map[int64]int
}

func (r explainLoads) PeekAccountsLoadBatch(_ context.Context, as []AccountWithConcurrency) (map[int64]*AccountLoadInfo, error) {
	out := map[int64]*AccountLoadInfo{}
	for _, a := range as {
		out[a.ID] = &AccountLoadInfo{AccountID: a.ID, CurrentConcurrency: r.counts[a.ID]}
	}
	return out, nil
}

type explainGroups struct {
	GroupRepository
	group *Group
}

func (r explainGroups) GetByIDLite(context.Context, int64) (*Group, error) { return r.group, nil }
func allowExplain(_ context.Context, _ SchedulingExplainInput, as []Account) (map[int64]SchedulingExplainEligibility, error) {
	out := map[int64]SchedulingExplainEligibility{}
	for _, a := range as {
		out[a.ID] = SchedulingExplainEligibility{true, "eligible"}
	}
	return out, nil
}
func expectExplainControl(m sqlmock.Sqlmock, id int64, scope, state string) {
	m.ExpectQuery("SELECT COALESCE").WithArgs(id).WillReturnRows(sqlmock.NewRows([]string{"family"}).AddRow(id))
	m.ExpectQuery("SELECT state,epoch,mode").WithArgs(scope, id).WillReturnRows(sqlmock.NewRows([]string{"state", "epoch", "mode", "session_deadline", "session_turn_limit", "updated_at"}).AddRow(state, 0, "request_drain", nil, 0, time.Now()))
	m.ExpectQuery("SELECT COUNT\\(\\*\\) FILTER").WithArgs(id).WillReturnRows(sqlmock.NewRows([]string{"active", "unknown", "pending"}).AddRow(0, 0, 0))
	m.ExpectQuery("SELECT COUNT\\(\\*\\) FROM scheduling_session_grants").WithArgs(scope, id, int64(0)).WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(0))
}
func TestExplainUsesInheritedProfilePinCapacityAndPreservesSharedState(t *testing.T) {
	ctx := context.Background()
	db, m, e := sqlmock.New()
	require.NoError(t, e)
	defer func() { _ = db.Close() }()
	mr := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	defer func() { require.NoError(t, client.Close()) }()
	scoped := scheduling.Policy{GroupID: 7, Model: "m", Enabled: true, Mode: scheduling.ModeSWRR, Version: 4, Profiles: []scheduling.LatencyProfile{{Name: "known-context", ContextMinTokens: 8192}}, Accounts: []scheduling.AccountRule{{AccountID: 1, Weight: 7}, {AccountID: 2, Weight: 3}, {AccountID: 3, Weight: 1}}}
	inherited := scheduling.LatencyProfile{Name: "ws-default", Transport: "ws", HealthThresholdMS: 8000, RecoveryThresholdMS: 6000, AttemptTimeoutMS: 12000, TotalBudgetMS: 30000, MinAttemptWindowMS: 6000}
	global := scheduling.Policy{Model: "m", Profiles: []scheduling.LatencyProfile{inherited}}
	for _, p := range []scheduling.Policy{scoped, global} {
		raw, e := json.Marshal(p)
		require.NoError(t, e)
		m.ExpectQuery("SELECT version, policy FROM scheduling_policies").WithArgs(p.GroupID, "m").WillReturnRows(sqlmock.NewRows([]string{"version", "policy"}).AddRow(p.Version, raw))
	}
	accounts := []Account{{ID: 1, Concurrency: 1}, {ID: 2, Concurrency: 3}, {ID: 3, Concurrency: 1}}
	m.ExpectQuery("SELECT account_id,COUNT").WillReturnRows(sqlmock.NewRows([]string{"account_id", "count"}).AddRow(1, 1))
	for _, a := range accounts {
		expectExplainControl(m, a.ID, scheduling.ScopeAccount, scheduling.ControlRunning)
		state := scheduling.ControlRunning
		if a.ID == 3 {
			state = scheduling.ControlPaused
		}
		expectExplainControl(m, a.ID, scheduling.ScopeFamily, state)
	}
	h := scheduling.HealthSnapshot{State: scheduling.HealthRecovering, RecoveryStage: 1, GoodStreak: 4, UpdatedAtMS: time.Now().UnixMilli()}
	raw, e := json.Marshal(h)
	require.NoError(t, e)
	require.NoError(t, client.Set(ctx, scheduling.HealthRedisKey(2, "m", inherited, "default", "unknown", "ws"), raw, 0).Err())
	svc := &ControlledSchedulingService{Store: scheduling.NewPostgresStore(db), Runtime: scheduling.NewRuntime(scheduling.NewRedisStore(client)), accounts: explainAccounts{accounts: accounts}, concurrency: NewConcurrencyService(explainLoads{counts: map[int64]int{2: 1}})}
	svc.SetExplainEligibility(allowExplain, nil)
	before := mr.Dump()
	out, e := svc.Explain(ctx, json.RawMessage("{\"group_id\":7,\"model\":\"m\",\"protocol\":\"ws\",\"pin_account_id\":2}"))
	require.NoError(t, e)
	require.Equal(t, before, mr.Dump())
	result, ok := out.(map[string]any)
	require.True(t, ok)
	require.Equal(t, "global", result["profile_source"])
	require.Equal(t, "unknown", result["context_bucket"])
	require.Equal(t, scheduling.ModePin, result["mode"])
	selected, ok := result["selected_account_id"].(*int64)
	require.True(t, ok)
	require.Equal(t, int64(2), *selected)
	rows, ok := result["candidates"].([]schedulingExplainRow)
	require.True(t, ok)
	require.Equal(t, 1, rows[0].CurrentConcurrency)
	require.False(t, rows[0].CapacityAvailable)
	require.Equal(t, 1, rows[1].CurrentConcurrency)
	require.Equal(t, 1, *rows[1].RecoveryStage)
	require.Equal(t, 4, rows[1].GoodStreak)
	require.False(t, rows[2].Eligible)
	require.Equal(t, "family_PAUSED", rows[2].Reason)
	require.NoError(t, m.ExpectationsWereMet())
}
func TestExplainMissingScopedPolicyInheritsGlobalWithoutWrites(t *testing.T) {
	db, m, e := sqlmock.New()
	require.NoError(t, e)
	defer func() { _ = db.Close() }()
	mr := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	defer func() { require.NoError(t, client.Close()) }()
	m.ExpectQuery("SELECT version, policy").WithArgs(int64(7), "m").WillReturnError(sql.ErrNoRows)
	raw, e := json.Marshal(scheduling.Policy{Model: "m", Version: 9, Enabled: true, Mode: scheduling.ModeFillFirst})
	require.NoError(t, e)
	m.ExpectQuery("SELECT version, policy").WithArgs(int64(0), "m").WillReturnRows(sqlmock.NewRows([]string{"version", "policy"}).AddRow(9, raw))
	svc := &ControlledSchedulingService{Store: scheduling.NewPostgresStore(db), Runtime: scheduling.NewRuntime(scheduling.NewRedisStore(client)), accounts: explainAccounts{}}
	before := mr.Dump()
	out, e := svc.Explain(context.Background(), json.RawMessage("{\"group_id\":7,\"model\":\"m\",\"context_tokens\":9000}"))
	require.NoError(t, e)
	require.Equal(t, before, mr.Dump())
	result, ok := out.(map[string]any)
	require.True(t, ok)
	require.Equal(t, "global", result["policy_source"])
	require.Equal(t, int64(9), result["policy_version"])
	require.Equal(t, "8k_32k", result["context_bucket"])
	selected, ok := result["selected_account_id"].(*int64)
	require.True(t, ok)
	require.Nil(t, selected)
	require.NoError(t, m.ExpectationsWereMet())
}
func TestExplainEligibilityMatchesGatewayHardGatesWithoutMutations(t *testing.T) {
	ctx := context.Background()
	group := &Group{ID: 7, Platform: PlatformAnthropic}
	svc := &GatewayService{groupRepo: explainGroups{group: group}}
	input := SchedulingExplainInput{GroupID: 7, Model: "claude-test"}
	base := Account{ID: 1, Status: StatusActive, Schedulable: true, Platform: PlatformAnthropic, Type: AccountTypeAPIKey, GroupIDs: []int64{7}}
	for _, mutate := range []func(*Account){func(*Account) {}, func(a *Account) { a.Schedulable = false }, func(a *Account) { a.GroupIDs = []int64{9} }, func(a *Account) { a.Platform = PlatformGemini }, func(a *Account) { a.Extra = map[string]any{"quota_limit": float64(1), "quota_used": float64(1)} }} {
		a := base
		mutate(&a)
		before, _ := json.Marshal(a)
		got, e := svc.ExplainSchedulingEligibility(ctx, input, []Account{a})
		require.NoError(t, e)
		expected, reason := svc.controlledGatewayEligibility(ctx, &a, &group.ID, group, input.Model, PlatformAnthropic, true)
		require.Equal(t, SchedulingExplainEligibility{expected, reason}, got[a.ID])
		after, _ := json.Marshal(a)
		require.Equal(t, before, after)
	}
}
func TestExplainRuntimePeekDoesNotDeleteExpiredEntries(t *testing.T) {
	now := time.Now()
	a := &Account{ID: 1, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Status: StatusActive, Schedulable: true}
	svc := &OpenAIGatewayService{openaiModelTransient: newOpenAIAccountModelTransientState(10), openaiProxyStreamCircuit: newOpenAIProxyStreamCircuit(openAIProxyStreamCircuitSettings{})}
	svc.openaiAccountRuntimeBlockUntil.Store(a.ID, now.Add(-time.Minute))
	key, _ := openAIAccountModelTransientKey(a.ID, "m")
	svc.openaiModelTransient.entries[key] = openAIAccountModelTransientEntry{lastFailure: now.Add(-time.Hour), blockUntil: now.Add(time.Hour)}
	require.False(t, svc.explainRuntimeBlocked(a, "m", now))
	_, ok := svc.openaiAccountRuntimeBlockUntil.Load(a.ID)
	require.True(t, ok)
	require.Len(t, svc.openaiModelTransient.entries, 1)
}

func TestExplainDoesNotStartQuotaSettingsRefresh(t *testing.T) {
	// An expired cached value is exactly what the live hot path receives. Explain
	// must neither replace it nor launch GetValue in a background goroutine.
	settings := &SettingService{settingRepo: explainNoSettingsReads{}}
	cached := &cachedOpenAIQuotaAutoPauseSettings{expiresAt: time.Now().Add(-time.Hour).UnixNano()}
	settings.openAIQuotaAutoPauseSettingsCache.Store(cached)
	svc := &OpenAIGatewayService{settingService: settings}
	_, e := svc.ExplainSchedulingEligibility(context.Background(), SchedulingExplainInput{Model: "m"}, nil)
	require.NoError(t, e)
	require.Same(t, cached, settings.openAIQuotaAutoPauseSettingsCache.Load())
}

type explainNoSettingsReads struct{ SettingRepository }

func (explainNoSettingsReads) GetValue(context.Context, string) (string, error) {
	panic("Explain started quota settings refresh")
}
