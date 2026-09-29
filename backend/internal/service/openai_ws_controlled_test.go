package service

import (
	"context"
	"database/sql"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/gin-gonic/gin"
	"github.com/liulixin-lex/xy2api/internal/scheduling"
	"github.com/stretchr/testify/require"
)

func TestOpenAIWSControlledTurnKeepsRetryAndResetsNextTurn(t *testing.T) {
	db, m, err := sqlmock.New()
	require.NoError(t, err)
	defer func() { _ = db.Close() }()
	svc := &OpenAIGatewayService{controlledScheduling: &ControlledSchedulingService{Store: scheduling.NewPostgresStore(db)}}
	ctx := NewControlledRequestContext(context.Background(), "responses")
	first := controlledRequest(ctx)
	first.policyLoaded = true
	first.Model = "model-a"
	first.SessionID = "registered-session"
	first.owner = true
	first.Policy, first.Profile = accountPoolPolicy(scheduling.DefaultGroupPolicy(0), "model-a")
	first.Ledger = scheduling.NewAttemptLedger(first.Policy.Retry, first.Profile, first.Started, first.ClientDeadline)
	require.NoError(t, first.Ledger.BeginAttempt(11, 0, time.Now(), true))
	first.Ledger.MarkFirstOutputTimeout()
	firstLedger := first.Ledger
	firstBudget := firstLedger.Snapshot()
	t.Cleanup(first.Close)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest("GET", "/v1/responses", nil)
	retry, err := svc.prepareControlledWSTurn(ctx, c, []byte(`{"model":"model-a"}`), "model-a", "new-untrusted-session", false)
	require.NoError(t, err)
	require.Same(t, first, controlledRequest(retry))
	require.Same(t, firstLedger, controlledRequest(retry).Ledger)
	require.Equal(t, firstBudget, controlledRequest(retry).Ledger.Snapshot(), "retry must preserve the same turn budget and timeout history")
	require.Equal(t, "registered-session", controlledRequest(retry).SessionID)
	m.ExpectQuery("SELECT version,policy FROM scheduling_group_policies").WithArgs(int64(0)).WillReturnError(sql.ErrNoRows)
	next, err := svc.prepareControlledWSTurn(ctx, c, []byte(`{"model":"model-b","reasoning":{"effort":"high"}}`), "model-b", "", true)
	require.NoError(t, err)
	r := controlledRequest(next)
	require.NotSame(t, first, r)
	require.NotEqual(t, first.ID, r.ID)
	require.Equal(t, "registered-session", r.SessionID)
	require.Equal(t, "high", r.Reasoning)
	require.Equal(t, "model-b", r.Model)
	require.True(t, r.owner)
	require.Equal(t, "ws", r.Protocol)
	require.NotNil(t, r.Ledger)
	require.NotSame(t, firstLedger, r.Ledger)
	require.Zero(t, r.Ledger.Snapshot().Attempts)
	require.False(t, r.Ledger.Snapshot().TimeoutSeen)
	require.True(t, r.Policy.Enabled)
	require.True(t, r.Policy.AccountPool)
	require.Equal(t, scheduling.DefaultGroupMaxAttempts, r.Policy.Retry.MaxAttempts)
	require.Equal(t, firstBudget, firstLedger.Snapshot(), "next turn cannot reset the preceding turn ledger")
	t.Cleanup(r.Close)
	require.NoError(t, m.ExpectationsWereMet())
}
func TestOpenAIWSControlledManualPauseRetainsOwner(t *testing.T) {
	ctx := context.Background()
	groupID := int64(23)
	account := Account{ID: 77, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Status: StatusActive, Schedulable: false, Concurrency: 2, Extra: map[string]any{"openai_apikey_responses_websockets_v2_enabled": true}}
	cache := &stubGatewayCache{}
	store := NewOpenAIWSStateStore(cache)
	svc := &OpenAIGatewayService{accountRepo: stubOpenAIAccountRepo{accounts: []Account{account}}, cache: cache, cfg: newOpenAIWSV2TestConfig(), concurrencyService: NewConcurrencyService(stubConcurrencyCache{}), openaiWSStateStore: store}
	require.NoError(t, store.BindResponseAccount(ctx, groupID, "resp_paused", account.ID, time.Hour))
	selection, err := svc.SelectAccountByPreviousResponseID(ctx, &groupID, "resp_paused", "gpt-5.1", nil, false)
	require.NoError(t, err)
	require.Nil(t, selection)
	id, err := store.GetResponseAccount(ctx, groupID, "resp_paused")
	require.NoError(t, err)
	require.Equal(t, account.ID, id)
	copy, allowed := svc.controlledOwnerForContinuation(ctx, &account)
	require.False(t, allowed)
	require.Same(t, &account, copy)
	require.False(t, account.Schedulable)
}
func TestOpenAIWSControlledForwardGateFailureSendsNothing(t *testing.T) {
	gin.SetMode(gin.TestMode)
	cfg := newOpenAIWSV2TestConfig()
	cfg.Security.URLAllowlist.Enabled = false
	cfg.Security.URLAllowlist.AllowInsecureHTTP = true
	cfg.Gateway.OpenAIWS.MaxConnsPerAccount = 1
	cfg.Gateway.OpenAIWS.MaxIdlePerAccount = 1
	conn := &openAIWSCaptureConn{}
	pool := newOpenAIWSConnPool(cfg)
	pool.setClientDialerForTest(&openAIWSCaptureDialer{conn: conn})
	defer pool.Close()
	svc := &OpenAIGatewayService{cfg: cfg, cache: &stubGatewayCache{}, openaiWSPool: pool, openaiWSResolver: NewOpenAIWSProtocolResolver(cfg), toolCorrector: NewCodexToolCorrector(), controlledScheduling: &ControlledSchedulingService{Store: scheduling.NewPostgresStore(nil)}}
	account := &Account{ID: 3, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Status: StatusActive, Schedulable: true, Concurrency: 1, Credentials: map[string]any{"api_key": "isolated", "base_url": "http://isolated.invalid"}, Extra: map[string]any{"openai_apikey_responses_websockets_v2_enabled": true}}
	ctx := NewControlledRequestContext(context.Background(), "responses")
	r := controlledRequest(ctx)
	r.policyLoaded = true
	r.Model = "gpt-5.1"
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil).WithContext(ctx)
	_, err := svc.forwardOpenAIWSV2(ctx, c, account, map[string]any{"model": "gpt-5.1", "input": "hello", "stream": true}, "", "", "isolated", OpenAIWSProtocolDecision{Transport: OpenAIUpstreamTransportResponsesWebsocketV2}, false, true, "gpt-5.1", "gpt-5.1", time.Now(), 0, "", nil)
	require.ErrorIs(t, err, scheduling.ErrSharedState)
	conn.mu.Lock()
	require.Empty(t, conn.writes)
	conn.mu.Unlock()
}
