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

// Group configuration is loaded once by group; model names never address a
// policy row. These expectations also forbid legacy policy or projection writes.
func expectExplainGroupPolicy(m sqlmock.Sqlmock, groupID int64, p *scheduling.GroupPolicy, accounts []Account) {
	m.ExpectBegin()
	if groupID > 0 {
		m.ExpectQuery("SELECT id FROM groups").WithArgs(groupID).WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(groupID))
	}
	query := m.ExpectQuery("SELECT version,policy FROM scheduling_group_policies").WithArgs(groupID)
	if p == nil {
		query.WillReturnError(sql.ErrNoRows)
	} else {
		raw, _ := json.Marshal(p)
		query.WillReturnRows(sqlmock.NewRows([]string{"version", "policy"}).AddRow(p.Version, raw))
	}
	members := sqlmock.NewRows([]string{"id", "priority"})
	for _, a := range accounts {
		members.AddRow(a.ID, a.Priority)
	}
	memberQuery := m.ExpectQuery("SELECT a.id,a.priority FROM accounts a")
	if groupID > 0 {
		memberQuery.WithArgs(groupID)
	}
	memberQuery.WillReturnRows(members)
	m.ExpectQuery("SELECT DISTINCT ON").WithArgs(groupID).WillReturnRows(sqlmock.NewRows([]string{"model", "policy"}))
	m.ExpectCommit()
}

func TestExplainGroupPolicyPinCapacityAndPreservesSharedState(t *testing.T) {
	ctx := context.Background()
	db, m, e := sqlmock.New()
	require.NoError(t, e)
	defer func() { _ = db.Close() }()
	mr := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	defer func() { require.NoError(t, client.Close()) }()
	zero, one := 0, 1
	p := scheduling.DefaultGroupPolicy(7)
	p.Version = 4
	p.Accounts = []scheduling.AccountRule{{AccountID: 1, Priority: &zero, Weight: 7}, {AccountID: 2, Priority: &zero, Weight: 3}, {AccountID: 3, Priority: &one, Weight: 1}}
	accounts := []Account{{ID: 1, Priority: 0, Concurrency: 1}, {ID: 2, Priority: 0, Concurrency: 3}, {ID: 3, Priority: 1, Concurrency: 1}}
	expectExplainGroupPolicy(m, 7, &p, accounts)
	m.ExpectQuery("SELECT account_id,COUNT").WillReturnRows(sqlmock.NewRows([]string{"account_id", "count"}).AddRow(1, 1))
	for _, a := range accounts {
		expectExplainControl(m, a.ID, scheduling.ScopeAccount, scheduling.ControlRunning)
		state := scheduling.ControlRunning
		if a.ID == 3 {
			state = scheduling.ControlPaused
		}
		expectExplainControl(m, a.ID, scheduling.ScopeFamily, state)
		if state == scheduling.ControlRunning {
			expectExplainFailureDomains(m, a.ID, false)
		}
	}
	require.NoError(t, client.Set(ctx, "explain-readonly-sentinel", "retained", 0).Err())
	svc := &ControlledSchedulingService{Store: scheduling.NewPostgresStore(db), Runtime: scheduling.NewRuntime(scheduling.NewRedisStore(client)), accounts: explainAccounts{accounts: accounts}, concurrency: NewConcurrencyService(explainLoads{counts: map[int64]int{2: 1}})}
	svc.SetExplainEligibility(allowExplain, nil)
	before := mr.Dump()
	out, e := svc.Explain(ctx, json.RawMessage(`{"group_id":7,"model":"m","protocol":"ws","pin_account_id":2}`))
	require.NoError(t, e)
	require.Equal(t, before, mr.Dump())
	result, ok := out.(map[string]any)
	require.True(t, ok)
	require.Equal(t, "group_accounts", result["policy_source"])
	require.Equal(t, "group_wait_budget", result["profile_source"])
	require.Equal(t, "unknown", result["context_bucket"])
	require.Equal(t, "account_weights", result["mode"])
	require.Equal(t, int64(4), result["policy_version"])
	selectedID, ok := result["selected_account_id"].(*int64)
	require.True(t, ok)
	require.NotNil(t, selectedID)
	require.Equal(t, int64(2), *selectedID)
	profile, ok := result["profile"].(scheduling.LatencyProfile)
	require.True(t, ok)
	require.Equal(t, scheduling.DefaultFirstOutputTimeoutMS, profile.AttemptTimeoutMS)
	require.Zero(t, profile.HealthThresholdMS)
	rows, ok := result["candidates"].([]schedulingExplainRow)
	require.True(t, ok)
	require.Equal(t, 1, rows[0].CurrentConcurrency)
	require.False(t, rows[0].CapacityAvailable)
	require.Equal(t, int64(7), rows[0].Weight)
	require.Equal(t, 1, rows[1].CurrentConcurrency)
	require.Nil(t, rows[1].RecoveryStage)
	require.False(t, rows[2].Eligible)
	require.Equal(t, "family_PAUSED", rows[2].Reason)
	require.NoError(t, m.ExpectationsWereMet())
}

func TestExplainUnconfiguredGroupProjectsAccountsWithoutGlobalInheritance(t *testing.T) {
	db, m, e := sqlmock.New()
	require.NoError(t, e)
	defer func() { _ = db.Close() }()
	mr := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	defer func() { require.NoError(t, client.Close()) }()
	accounts := []Account{{ID: 1, Priority: 17, Concurrency: 3}}
	expectExplainGroupPolicy(m, 7, nil, accounts)
	m.ExpectQuery("SELECT account_id,COUNT").WillReturnRows(sqlmock.NewRows([]string{"account_id", "count"}))
	expectExplainControl(m, 1, scheduling.ScopeAccount, scheduling.ControlRunning)
	expectExplainControl(m, 1, scheduling.ScopeFamily, scheduling.ControlRunning)
	expectExplainFailureDomains(m, 1, false)
	svc := &ControlledSchedulingService{Store: scheduling.NewPostgresStore(db), Runtime: scheduling.NewRuntime(scheduling.NewRedisStore(client)), accounts: explainAccounts{accounts: accounts}, concurrency: NewConcurrencyService(explainLoads{})}
	svc.SetExplainEligibility(allowExplain, nil)
	before := mr.Dump()
	out, e := svc.Explain(context.Background(), json.RawMessage(`{"group_id":7,"model":"different-model","context_tokens":9000}`))
	require.NoError(t, e)
	require.Equal(t, before, mr.Dump())
	result, ok := out.(map[string]any)
	require.True(t, ok)
	require.Equal(t, "group_accounts", result["policy_source"])
	require.Equal(t, int64(0), result["policy_version"])
	require.Equal(t, "8k_32k", result["context_bucket"])
	rows, ok := result["candidates"].([]schedulingExplainRow)
	require.True(t, ok)
	require.Equal(t, 17, rows[0].Priority)
	require.Equal(t, int64(1), rows[0].Weight)
	selectedID, ok := result["selected_account_id"].(*int64)
	require.True(t, ok)
	require.NotNil(t, selectedID)
	require.Equal(t, int64(1), *selectedID)
	require.NoError(t, m.ExpectationsWereMet())
}

func TestLoadGroupPolicyIsSharedAcrossModelsAndIndependentAcrossGroups(t *testing.T) {
	db, m, e := sqlmock.New()
	require.NoError(t, e)
	defer func() { _ = db.Close() }()
	svc := &ControlledSchedulingService{Store: scheduling.NewPostgresStore(db)}
	priority := -2
	configured := scheduling.DefaultGroupPolicy(7)
	configured.Version = 4
	configured.FirstOutputTimeoutMS = 3000
	configured.TotalWaitTimeoutMS = 9000
	configured.Accounts = []scheduling.AccountRule{{AccountID: 1, Priority: &priority, Weight: 7}}
	for _, tc := range []struct {
		group      int64
		model      string
		configured bool
	}{{7, "model-a", true}, {7, "model-b", true}, {9, "model-a", false}} {
		query := m.ExpectQuery("SELECT version,policy FROM scheduling_group_policies").WithArgs(tc.group)
		if tc.configured {
			raw, err := json.Marshal(configured)
			require.NoError(t, err)
			query.WillReturnRows(sqlmock.NewRows([]string{"version", "policy"}).AddRow(configured.Version, raw))
		} else {
			query.WillReturnError(sql.ErrNoRows)
		}
		ctx := NewControlledRequestContext(context.Background(), "responses")
		r, on, err := svc.loadPolicy(ctx, &tc.group, tc.model, "")
		require.NoError(t, err)
		require.True(t, on)
		require.Equal(t, tc.model, r.Policy.Model)
		require.Equal(t, tc.group, r.Policy.GroupID)
		if tc.configured {
			require.Equal(t, configured.Accounts, r.Policy.Accounts)
			require.Equal(t, int64(3000), r.Profile.AttemptTimeoutMS)
			require.Equal(t, int64(4), r.Policy.Version)
		} else {
			require.Empty(t, r.Policy.Accounts)
			require.Equal(t, scheduling.DefaultFirstOutputTimeoutMS, r.Profile.AttemptTimeoutMS)
			require.Zero(t, r.Policy.Version)
		}
		r.Close()
	}
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

func expectExplainFailureDomains(m sqlmock.Sqlmock, id int64, blocked bool) {
	m.ExpectQuery("SELECT c.id,c.credentials").WithArgs(id).WillReturnRows(sqlmock.NewRows([]string{"credential_owner_id", "credentials", "extra", "platform", "type", "version", "quota_pool_id", "availability_pool_id"}).AddRow(id, []byte("{}"), []byte("{}"), "", "", 0, "", ""))
	rows := sqlmock.NewRows([]string{"gate_key", "scope", "reason", "version", "blocked", "ready_after", "probe_active"})
	if blocked {
		rows.AddRow("local-gate", "logical_account", "credential_auth", 1, true, nil, false)
	}
	m.ExpectQuery("SELECT gate_key,scope,reason,version").WillReturnRows(rows)
}
func TestExplainFailureDomainGateMatchesLiveEligibility(t *testing.T) {
	db, m, e := sqlmock.New()
	require.NoError(t, e)
	defer func() { _ = db.Close() }()
	mr := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	defer func() { require.NoError(t, client.Close()) }()
	p := scheduling.DefaultGroupPolicy(0)
	p.Version = 1
	accounts := []Account{{ID: 1, Concurrency: 3}}
	expectExplainGroupPolicy(m, 0, &p, accounts)
	m.ExpectQuery("SELECT account_id,COUNT").WillReturnRows(sqlmock.NewRows([]string{"account_id", "count"}))
	expectExplainControl(m, 1, scheduling.ScopeAccount, scheduling.ControlRunning)
	expectExplainControl(m, 1, scheduling.ScopeFamily, scheduling.ControlRunning)
	expectExplainFailureDomains(m, 1, true)
	svc := &ControlledSchedulingService{Store: scheduling.NewPostgresStore(db), Runtime: scheduling.NewRuntime(scheduling.NewRedisStore(client)), accounts: explainAccounts{accounts: []Account{{ID: 1, Concurrency: 3}}}, concurrency: NewConcurrencyService(explainLoads{})}
	svc.SetExplainEligibility(allowExplain, nil)
	before := mr.Dump()
	out, e := svc.Explain(context.Background(), json.RawMessage(`{"model":"m","protocol":"responses"}`))
	require.NoError(t, e)
	result, ok := out.(map[string]any)
	require.True(t, ok)
	rows, ok := result["candidates"].([]schedulingExplainRow)
	require.True(t, ok)
	require.False(t, rows[0].Eligible)
	require.Equal(t, "failure_domain_gate", rows[0].Reason)
	profile, ok := result["profile"].(scheduling.LatencyProfile)
	require.True(t, ok)
	require.Equal(t, scheduling.AccountPoolProfileName, profile.Name)
	require.Equal(t, false, result["context_tokens_known"])
	require.Equal(t, "default", result["reasoning_effort"])
	require.Equal(t, before, mr.Dump())
	require.NoError(t, m.ExpectationsWereMet())
}
