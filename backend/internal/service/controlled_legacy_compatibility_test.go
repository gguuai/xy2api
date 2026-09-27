package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/liulixin-lex/xy2api/internal/config"
	"github.com/liulixin-lex/xy2api/internal/scheduling"
	"github.com/stretchr/testify/require"
)

func controlledLegacyTestContext() context.Context {
	ctx := NewControlledRequestContext(context.Background(), "http")
	r := controlledRequest(ctx)
	r.policyLoaded = true
	r.Policy.Enabled = true
	return ctx
}

func TestControlledLegacyHealthIsNotMutated(t *testing.T) {
	ctx := controlledLegacyTestContext()
	encoded, err := json.Marshal(OpenAIAPIKeyHealthBreakerSettings{Enabled: true, WindowMinutes: 1, FailureThreshold: 3, CooldownMinutes: 5})
	require.NoError(t, err)
	settingsRepo := &openAIAPIKeyHealthSettingRepo{value: string(encoded)}
	cache := &openAIAPIKeyHealthCacheStub{tripped: true}
	repo := &openAIAPIKeyHealthAccountRepo{}
	blocker := &openAIAPIKeyHealthRuntimeBlocker{}
	rate := NewRateLimitService(repo, nil, &config.Config{}, nil, cache)
	rate.SetSettingService(NewSettingService(settingsRepo, &config.Config{}))
	rate.SetOpenAIAPIKeyHealthCache(cache)
	rate.SetAccountRuntimeBlocker(blocker)
	gateway := &OpenAIGatewayService{rateLimitService: rate}
	account := openAIHealthPoolAccount()
	failure := &UpstreamFailoverError{StatusCode: 502}
	require.False(t, rate.ObserveOpenAIAPIKeyHealthFailure(ctx, account, failure))
	require.False(t, gateway.ReportOpenAIAccountScheduleResultWithContext(ctx, account, "gpt-5", false, nil, failure))
	require.False(t, gateway.ReportOpenAIAccountScheduleResultWithContext(ctx, account, "gpt-5", true, nil))
	require.False(t, gateway.ObserveOpenAIAccountHealthFailure(ctx, account, failure))
	require.False(t, rate.HandleStreamTimeout(ctx, account, "gpt-5"))
	require.Zero(t, settingsRepo.getCalls)
	require.Zero(t, cache.recordCalls)
	require.Zero(t, repo.setCalls)
	require.Zero(t, blocker.calls)
	require.Nil(t, account.TempUnschedulableUntil)
}

type controlledHardFailureRepo struct {
	AccountRepository
	errors int
}

func (r *controlledHardFailureRepo) SetError(context.Context, int64, string) error {
	r.errors++
	return nil
}

func TestControlledLegacySoftFailureAndHardAuth(t *testing.T) {
	ctx := controlledLegacyTestContext()
	repo := &controlledHardFailureRepo{}
	rate := NewRateLimitService(repo, nil, &config.Config{}, nil, nil)
	account := &Account{ID: 42, Platform: PlatformOpenAI, Type: AccountTypeAPIKey}
	for _, status := range []int{500, 502, 503, 529} {
		require.Equal(t, ErrorPolicyNone, rate.CheckErrorPolicy(ctx, account, status, []byte("upstream failed")))
		require.False(t, rate.HandleUpstreamError(ctx, account, status, http.Header{}, []byte("upstream failed")))
	}
	require.Zero(t, repo.errors)
	require.True(t, rate.HandleUpstreamError(ctx, account, http.StatusUnauthorized, http.Header{}, []byte("unauthorized")))
	require.Equal(t, 1, repo.errors, "credential rejection must remain a hard gate")
}

func TestControlledStopsRetainIdentityAcrossAdapters(t *testing.T) {
	ctx := controlledLegacyTestContext()
	account := &Account{ID: 42, Platform: PlatformOpenAI, Type: AccountTypeAPIKey}
	for _, sentinel := range []error{scheduling.ErrCommitted, scheduling.ErrAttemptBudget, scheduling.ErrDeadline, scheduling.ErrRetryBudget, scheduling.ErrUnsafeReplay, scheduling.ErrSharedState, scheduling.ErrControlBlocked, scheduling.ErrCapacity} {
		t.Run(sentinel.Error(), func(t *testing.T) {
			wrapped := fmt.Errorf("dispatch gate: %w", sentinel)
			require.True(t, IsControlledSchedulingStop(wrapped))
			// nil Gin/service dependencies prove this path does not touch upstream health or Ops.
			got := (&GatewayService{}).handleUpstreamTransportError(ctx, nil, account, wrapped, OpsUpstreamErrorEvent{})
			require.ErrorIs(t, got, sentinel)
			got = (&OpenAIGatewayService{}).handleOpenAIUpstreamTransportError(ctx, nil, account, wrapped, false)
			require.ErrorIs(t, got, sentinel)
			got = (&GeminiMessagesCompatService{}).handleUpstreamTransportError(ctx, nil, account, wrapped)
			require.ErrorIs(t, got, sentinel)
			var failover *UpstreamFailoverError
			require.False(t, errors.As(got, &failover))
		})
	}
}

func TestControlledAntigravityMakesOneAttempt(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, status := range []int{429, 500, 503} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			ctx := controlledLegacyTestContext()
			body := []byte("{\"model\":\"gemini-3-pro-high\",\"request\":{\"contents\":[{\"parts\":[{\"text\":\"original context\"}]}]}}")
			responseBody := "{\"error\":{\"code\":500,\"status\":\"INTERNAL\",\"message\":\"Internal error encountered.\"}}"
			upstream := &queuedHTTPUpstreamStub{responses: []*http.Response{{StatusCode: status, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(responseBody))}}}
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			c.Request = httptest.NewRequest("POST", "/v1/messages", nil).WithContext(ctx)
			counter := &recordingInternal500CounterCache{}
			svc := &AntigravityGatewayService{internal500Cache: counter}
			handled := 0
			result, err := svc.antigravityRetryLoop(antigravityRetryLoopParams{ctx: ctx, prefix: "test", account: &Account{ID: 1, Platform: PlatformAntigravity, Type: AccountTypeOAuth}, accessToken: "test", action: "generateContent", body: body, c: c, httpUpstream: upstream,
				handleError: func(context.Context, string, *Account, int, http.Header, []byte, string, int64, string, bool) *handleModelRateLimitResult {
					handled++
					return nil
				}})
			require.NoError(t, err)
			require.Equal(t, status, result.resp.StatusCode)
			require.Equal(t, 1, upstream.callCount)
			require.JSONEq(t, string(body), string(upstream.requestBodies[0]))
			require.Empty(t, counter.incrementCalls)
			if status == 429 {
				require.Equal(t, 1, handled, "real rate limit processing remains enabled")
			}
			require.NoError(t, result.resp.Body.Close())
		})
	}
}

func TestControlledGatewayPassthroughDoesNotRetry(t *testing.T) {
	gin.SetMode(gin.TestMode)
	ctx := controlledLegacyTestContext()
	body := []byte("{\"model\":\"claude-sonnet-4-5\",\"max_tokens\":1024,\"messages\":[{\"role\":\"user\",\"content\":\"original context\"}]}")
	upstream := &queuedHTTPUpstreamStub{responses: []*http.Response{{StatusCode: 502, Header: http.Header{}, Body: io.NopCloser(strings.NewReader("{\"error\":{\"message\":\"unavailable\"}}"))}}}
	svc := &GatewayService{cfg: &config.Config{}, httpUpstream: upstream, tlsFPProfileService: &TLSFingerprintProfileService{}}
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest("POST", "/v1/messages", nil).WithContext(ctx)
	account := &Account{ID: 1, Platform: PlatformAnthropic, Type: AccountTypeAPIKey, Credentials: map[string]any{"api_key": "test", "base_url": "https://api.anthropic.com"}}
	result, err := svc.forwardAnthropicAPIKeyPassthrough(ctx, c, account, body, "claude-sonnet-4-5", "claude-sonnet-4-5", false, time.Now())
	require.Nil(t, result)
	var failover *UpstreamFailoverError
	require.ErrorAs(t, err, &failover)
	require.Equal(t, 1, upstream.callCount)
	require.JSONEq(t, string(body), string(upstream.requestBodies[0]))
	require.False(t, c.Writer.Written())
}

func TestControlledSignatureAndCompactFallbackRemainDisabled(t *testing.T) {
	ctx := controlledLegacyTestContext()
	require.False(t, (&GatewayService{}).shouldRectifySignatureError(ctx, &Account{Type: AccountTypeAPIKey}, []byte("invalid thinking signature"), "claude-sonnet-4-5"))
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest("POST", "/v1/responses/compact", nil).WithContext(ctx)
	body := []byte("{\"model\":\"gpt-5\",\"input\":\"same context\"}")
	next, model, retry := (&OpenAIGatewayService{}).prepareOpenAICompactFallbackRetry(c, &Account{}, "gpt-5", body, 400, "model not found", nil, false)
	require.False(t, retry)
	require.Empty(t, model)
	require.Equal(t, body, next)
}

func TestControlledFailureDomainsRequireKnownTeamAndModel(t *testing.T) {
	ctx := controlledLegacyTestContext()
	r := controlledRequest(ctx)
	r.Ledger = scheduling.NewAttemptLedger(scheduling.DefaultRetryPolicy(), scheduling.LatencyProfile{}, time.Now(), time.Time{})
	account := &Account{ID: 1, Platform: PlatformGrok, Type: AccountTypeOAuth, Credentials: map[string]any{"team_id": "known-team"}}
	sibling := &Account{ID: 2, Platform: PlatformGrok, Type: AccountTypeOAuth, Credentials: map[string]any{"team_id": "known-team"}}
	independent := &Account{ID: 3, Platform: PlatformGrok, Type: AccountTypeOAuth, Credentials: map[string]any{"team_id": "other-team"}}
	blockControlledGrokTeamModelLimit(ctx, account, "grok-4.5")
	domains := r.Ledger.Snapshot().BlockedFailureDomains
	require.Len(t, domains, 1)
	require.True(t, domains[controlledAccountFailureDomains(sibling, "grok-4.5")[0]])
	require.False(t, domains[controlledAccountFailureDomains(independent, "grok-4.5")[0]])
	require.False(t, domains[controlledAccountFailureDomains(sibling, "grok-3")[0]])
	require.Empty(t, controlledAccountFailureDomains(&Account{Platform: PlatformGemini, Credentials: map[string]any{"project_id": "shared"}}, "gemini-2.5-pro"))
	require.Empty(t, controlledAccountFailureDomains(&Account{Platform: PlatformGrok, Type: AccountTypeOAuth}, "grok-4.5"))
}

func TestControlledAntigravityTransportFailurePreservesCrossAccountRetry(t *testing.T) {
	ctx := controlledLegacyTestContext()
	for _, upstreamErr := range []error{errors.New("connection reset"), fmt.Errorf("first_output_timeout: %w", context.DeadlineExceeded), scheduling.ErrAttemptBudget, &UpstreamFailoverError{StatusCode: 503, PreDispatchSelectionInvalidated: true}} {
		t.Run(upstreamErr.Error(), func(t *testing.T) {
			upstream := &queuedHTTPUpstreamStub{errors: []error{upstreamErr}}
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil).WithContext(ctx)
			_, err := (&AntigravityGatewayService{}).antigravityRetryLoop(antigravityRetryLoopParams{ctx: ctx, account: &Account{ID: 1, Platform: PlatformAntigravity}, action: "streamGenerateContent", body: []byte("{}"), c: c, httpUpstream: upstream})
			require.Equal(t, 1, upstream.callCount)
			require.False(t, c.Writer.Written())
			if IsControlledSchedulingStop(upstreamErr) {
				require.ErrorIs(t, err, upstreamErr)
				return
			}
			var failover *UpstreamFailoverError
			require.ErrorAs(t, err, &failover)
			if strings.Contains(upstreamErr.Error(), "first_output_timeout") {
				require.Equal(t, 504, failover.StatusCode)
			}
			if original, ok := upstreamErr.(*UpstreamFailoverError); ok {
				require.Same(t, original, failover)
			}
		})
	}
}

func TestControlledAgentIdentityRepairsTaskWithoutInferenceReplay(t *testing.T) {
	for _, passthrough := range []bool{false, true} {
		name := "responses"
		if passthrough {
			name = "passthrough"
		}
		t.Run(name, func(t *testing.T) {
			key, privateKey := newTestAgentIdentityKey(t)
			account := &Account{ID: 2323, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Status: StatusActive, Schedulable: true, Concurrency: 1, Credentials: map[string]any{"auth_mode": OpenAIAuthModeAgentIdentity, "agent_runtime_id": key.runtimeID, "agent_private_key": privateKey, "task_id": "old-task", "chatgpt_account_id": "test-account"}}
			if passthrough {
				account.Extra = map[string]any{"openai_passthrough": true}
			}
			repo := &accountTestAgentIdentityRepo{account: account}
			registerCalls := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				registerCalls++
				_, _ = io.WriteString(w, "{\"task_id\":\"repaired-task\"}")
			}))
			defer server.Close()
			oldBase := openAIAgentIdentityAuthAPIBaseURL
			openAIAgentIdentityAuthAPIBaseURL = server.URL
			defer func() { openAIAgentIdentityAuthAPIBaseURL = oldBase }()
			upstream := &httpUpstreamRecorder{responses: []*http.Response{{StatusCode: 401, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader("{\"error\":{\"code\":\"invalid_task_id\"}}"))}}}
			svc := &OpenAIGatewayService{cfg: &config.Config{}, accountRepo: repo, httpUpstream: upstream}
			ctx := controlledLegacyTestContext()
			body := []byte("{\"model\":\"gpt-5.4\",\"instructions\":\"Reply OK\",\"input\":[],\"stream\":false}")
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(string(body))).WithContext(ctx)
			_, err := svc.Forward(ctx, c, account, body)
			var failover *UpstreamFailoverError
			require.ErrorAs(t, err, &failover)
			require.Equal(t, GatewayFailureReason("agent_task_repaired"), failover.Reason)
			require.True(t, failover.ShouldRetryNextAccount())
			require.Equal(t, 1, registerCalls)
			require.Len(t, upstream.requests, 1, "task repair must not replay inference on the same account")
			require.Equal(t, "repaired-task", account.GetCredential("task_id"))
			require.Zero(t, repo.setErrorCalls)
			require.False(t, c.Writer.Written())
		})
	}
}
