package service

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/liulixin-lex/xy2api/internal/config"
	"github.com/liulixin-lex/xy2api/internal/domain"
	"github.com/liulixin-lex/xy2api/internal/pkg/iqcheck"
	"github.com/liulixin-lex/xy2api/internal/pkg/tlsfingerprint"
	"github.com/stretchr/testify/require"
)

type iqProbeAccounts struct {
	AccountRepository
	account *Account
}

func (r *iqProbeAccounts) GetByID(context.Context, int64) (*Account, error) { return r.account, nil }

type iqProbeTransport struct {
	tlsCalls int
	HTTPUpstream
	status   int
	body     string
	headers  http.Header
	failure  error
	requests []*http.Request
	bodies   []map[string]any
}

func (u *iqProbeTransport) Do(req *http.Request, _ string, _ int64, _ int) (*http.Response, error) {
	return u.doProbe(req)
}
func (u *iqProbeTransport) DoWithTLS(req *http.Request, _ string, _ int64, _ int, _ *tlsfingerprint.Profile) (*http.Response, error) {
	u.tlsCalls++
	return u.doProbe(req)
}
func (u *iqProbeTransport) doProbe(req *http.Request) (*http.Response, error) {
	u.requests = append(u.requests, req)
	var body map[string]any
	if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
		return nil, err
	}
	u.bodies = append(u.bodies, body)
	if u.failure != nil {
		return nil, u.failure
	}
	headers := u.headers
	if headers == nil {
		headers = http.Header{"Content-Type": []string{"application/json"}}
	}
	return &http.Response{StatusCode: u.status, Header: headers, Body: io.NopCloser(strings.NewReader(u.body))}, nil
}
func TestIQCheckProbe(t *testing.T) {
	for _, tc := range []struct {
		name         string
		httpStatus   int
		body, status string
		failure      error
	}{
		{"smart", 200, `{"choices":[{"index":0,"message":{"content":"{\"answer\":21}"},"finish_reason":"stop"}]}`, "smart", nil},
		{"explained", 200, `{"choices":[{"index":0,"message":{"content":"答案是21。解释如下。"},"finish_reason":"stop"}]}`, "smart", nil},
		{"wrong", 200, `{"choices":[{"index":0,"message":{"content":"29"},"finish_reason":"stop"}]}`, "degraded", nil},
		{"unauthorized", 401, "", "unknown", nil}, {"quota", 429, "", "unknown", nil}, {"server", 503, "", "unknown", nil}, {"unsupported", 400, "", "unknown", nil}, {"empty", 200, "{}", "unknown", nil}, {"timeout", 0, "", "unknown", context.DeadlineExceeded}, {"disconnected", 0, "", "unknown", io.ErrUnexpectedEOF},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a := &Account{ID: 1, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Status: StatusActive, Schedulable: true, Extra: map[string]any{"openai_responses_mode": "force_chat_completions"}, Credentials: map[string]any{"api_key": "fixture", "base_url": "https://example.test", "model_mapping": map[string]any{iqcheck.Model: "other-model"}}}
			transport := &iqProbeTransport{status: tc.httpStatus, body: tc.body, failure: tc.failure}
			s := &IQCheckService{accounts: &iqProbeAccounts{account: a}, tester: &AccountTestService{cfg: &config.Config{}, httpUpstream: transport}}
			for i := 0; i < 2; i++ {
				result := s.probe(context.Background(), 1)
				require.Equal(t, tc.status, result.Status, result)
				if tc.name == "timeout" {
					require.Equal(t, "timeout", result.Reason)
				}
				require.Nil(t, transport.requests[i].GetBody)
				require.True(t, HTTPUpstreamSingleAttempt(transport.requests[i].Context()))
				require.Equal(t, "single_attempt", result.Diagnostic.RetryVisibility)
				require.Equal(t, iqcheck.Model, transport.bodies[i]["model"])
				require.Equal(t, false, transport.bodies[i]["store"])
				require.Equal(t, iqcheck.Effort, transport.bodies[i]["reasoning_effort"])
				require.NotContains(t, transport.bodies[i], "previous_response_id")
				require.NotContains(t, transport.bodies[i], "conversation")
			}
			requireIQProbePayloadIsolation(t, a, transport.bodies)
			require.Equal(t, StatusActive, a.Status)
			require.True(t, a.Schedulable)
		})
	}
}
func TestIQCheckSchedulingGate(t *testing.T) {
	a := Account{Platform: PlatformOpenAI, Status: StatusActive, Schedulable: true, IQCheck: domain.IQCheck{Enabled: true, Status: "smart"}}
	require.True(t, a.IsSchedulable())
	a.IQCheck.Status = "degraded"
	require.False(t, a.IsSchedulable())
	require.False(t, a.IsCredentialUsableForShadow())
	a.IQCheck.Status = "smart"
	require.True(t, a.IsSchedulable())
	a.IQCheck.Status = "unknown"
	require.True(t, a.IsSchedulable())
	a.Schedulable = false
	require.False(t, a.IsSchedulable())
	a.IQCheck.Enabled = false
	require.False(t, a.IsSchedulable())
	a.Schedulable = true
	a.Status = "inactive"
	require.False(t, a.IsSchedulable())
	a.Status = StatusActive
	future := time.Now().Add(time.Hour)
	a.RateLimitResetAt = &future
	require.False(t, a.IsSchedulable())
	a.RateLimitResetAt = nil
	past := time.Now().Add(-time.Hour)
	a.ExpiresAt = &past
	a.AutoPauseOnExpired = true
	require.False(t, a.IsSchedulable())
}

func TestIQCheckOAuthPayloadAndStaleCandidate(t *testing.T) {
	for _, kind := range []string{AccountTypeOAuth, AccountTypeSetupToken, AccountTypeAPIKey} {
		t.Run(kind, func(t *testing.T) {
			a := &Account{ID: 1, Platform: PlatformOpenAI, Type: kind, Status: StatusActive, Schedulable: true, Credentials: map[string]any{"access_token": "fixture", "api_key": "fixture"}}
			transport := &iqProbeTransport{status: 200, body: `{"status":"completed","output":[{"type":"message","role":"assistant","phase":"final_answer","content":[{"type":"output_text","text":"{\"answer\":21}"}]}]}`}
			s := &IQCheckService{accounts: &iqProbeAccounts{account: a}, tester: &AccountTestService{cfg: &config.Config{}, httpUpstream: transport}}
			result := s.probe(context.Background(), 1)
			require.Equal(t, "smart", result.Status)
			require.Equal(t, iqcheck.Model, transport.bodies[0]["model"])
			require.Equal(t, map[string]any{"effort": iqcheck.Effort}, transport.bodies[0]["reasoning"])
			if kind == AccountTypeAPIKey {
				require.NotEmpty(t, transport.requests[0].Header.Get("Session_id"))
				require.Equal(t, transport.bodies[0]["prompt_cache_key"], transport.requests[0].Header.Get("Conversation_id"))
			} else {
				require.Empty(t, transport.requests[0].Header.Get("Session_id"))
				require.Empty(t, transport.requests[0].Header.Get("Conversation_id"))
			}
		})
	}
	cached := Account{ID: 2, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Status: StatusActive, Schedulable: true, IQCheck: domain.IQCheck{Enabled: true, Status: "smart"}}
	fresh := cached
	fresh.IQCheck.Status = "degraded"
	gateway := &OpenAIGatewayService{accountRepo: &iqProbeAccounts{account: &fresh}, schedulerSnapshot: &SchedulerSnapshotService{}}
	require.Nil(t, gateway.recheckSelectedOpenAIAccountFromDB(context.Background(), &cached, nil, PlatformOpenAI, iqcheck.Model, false, ""))
	for _, status := range []string{"smart", "unknown"} {
		fresh.IQCheck.Status = status
		fresh.IQCheck.LastRunStatus = "unknown"
		fresh.IQCheck.LastRunReason = "response_too_large"
		require.Nil(t, gateway.recheckSelectedOpenAIAccountFromDB(context.Background(), &cached, nil, PlatformOpenAI, iqcheck.Model, false, ""), "stale smart cache must not dispatch an account with a failed probe")
	}
	fresh.IQCheck.ObserveResult("smart", "correct_answer", time.Now())
	require.NotNil(t, gateway.recheckSelectedOpenAIAccountFromDB(context.Background(), &cached, nil, PlatformOpenAI, iqcheck.Model, false, ""))
}

func TestIQCheckConfiguredRoutes(t *testing.T) {
	for _, route := range []struct{ kind, mode, protocol string }{
		{AccountTypeOAuth, "", "responses"}, {AccountTypeAPIKey, "force_responses", "responses"}, {AccountTypeAPIKey, "force_chat_completions", "chat_completions"},
	} {
		t.Run(route.kind+route.mode, func(t *testing.T) {
			a := &Account{ID: 1, Platform: PlatformOpenAI, Type: route.kind, Credentials: map[string]any{"api_key": "fixture", "access_token": "fixture", "base_url": "https://example.test", "model_mapping": map[string]any{"custom/model": "wrong"}}, Extra: map[string]any{"openai_responses_mode": route.mode}, IQCheck: domain.IQCheck{Revision: "revision", Model: "custom/model", ReasoningEffort: "upstream_default", OutputMode: "strict"}}
			body := `{"status":"completed","model":"upstream-reported","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"21"}]}]}`
			if route.protocol == "chat_completions" {
				body = `{"model":"upstream-reported","choices":[{"index":0,"message":{"content":"21"},"finish_reason":"stop"}]}`
			}
			transport := &iqProbeTransport{status: 200, body: body}
			s := &IQCheckService{accounts: &iqProbeAccounts{account: a}, tester: &AccountTestService{cfg: &config.Config{}, httpUpstream: transport}}
			claim := IQCheckClaim{AccountID: 1, Revision: "revision", Profile: a.IQCheck.Profile(), Protocol: route.protocol}
			for range 2 {
				result := s.probe(context.Background(), 1, claim)
				require.Equal(t, "smart", result.Status, result)
				require.False(t, result.FormatCompliant)
				require.Equal(t, "upstream-reported", result.ReportedModel)
			}
			requireIQProbePayloadIsolation(t, a, transport.bodies)
			payload := transport.bodies[0]
			require.Equal(t, "custom/model", payload["model"])
			require.NotContains(t, payload, "reasoning")
			require.NotContains(t, payload, "reasoning_effort")
			require.NotContains(t, payload, "previous_response_id")
			if route.protocol == "responses" {
				require.Contains(t, payload, "text")
			} else {
				require.Contains(t, payload, "response_format")
			}
			a.IQCheck.Revision = "changed"
			require.Equal(t, "unknown", s.probe(context.Background(), 1, claim).Status)
			require.Len(t, transport.requests, 2)
		})
	}
}

func TestIQCheckCompleteOAuthStream(t *testing.T) {
	for _, kind := range []string{AccountTypeOAuth, AccountTypeSetupToken, AccountTypeAPIKey} {
		t.Run(kind, func(t *testing.T) {
			a := &Account{ID: 1, Platform: PlatformOpenAI, Type: kind, Status: StatusActive, Schedulable: true, Credentials: map[string]any{"access_token": "fixture", "api_key": "fixture"}, Extra: map[string]any{"openai_responses_mode": "force_responses"}}
			stream := "event: response.created\ndata: {\"type\":\"response.created\",\"response\":{\"status\":\"in_progress\"}}\n\n" +
				"data: {\"type\":\"response.auxiliary\",\"response\":\"extension\"}\n\n" +
				"data: {\"type\":\"response.output_text.delta\",\"delta\":\"29\"}\n\n" +
				"data: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\",\"output\":[{\"type\":\"message\",\"role\":\"assistant\",\"content\":[{\"type\":\"output_text\",\"text\":\"21\"}]}]}}\n\n"
			u := &iqProbeTransport{status: 200, body: stream, headers: http.Header{"Content-Type": {"text/event-stream; charset=utf-8"}, "X-Request-Id": {"fixture-id"}}}
			s := &IQCheckService{accounts: &iqProbeAccounts{account: a}, tester: &AccountTestService{cfg: &config.Config{}, httpUpstream: u}}
			for range 2 {
				result := s.probe(context.Background(), 1)
				require.Equal(t, "smart", result.Status)
				require.Equal(t, "21", result.Answer)
				require.Equal(t, "fixture-id", result.Diagnostic.RequestID)
				require.Equal(t, 4, result.Diagnostic.EventIndex)
			}
			requireIQProbePayloadIsolation(t, a, u.bodies)
			require.NotContains(t, u.bodies[0], "previous_response_id")
			require.Equal(t, false, u.bodies[0]["store"])
			require.Equal(t, StatusActive, a.Status)
			require.True(t, a.Schedulable)
			u.body = "data: {bad}\n\n"
			result := s.probe(context.Background(), 1)
			require.Equal(t, "unknown", result.Status)
			require.Equal(t, "invalid_event_json", result.Reason)
			require.NotNil(t, result.Diagnostic)
		})
	}
}
