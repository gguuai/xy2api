//go:build unit

package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/liulixin-lex/xy2api/internal/pkg/tlsfingerprint"
	"github.com/liulixin-lex/xy2api/internal/scheduling"
	"github.com/stretchr/testify/require"
)

type controlledBridgeUpstream struct {
	t            *testing.T
	server       *httptest.Server
	wantBuffered bool
	called       bool
	observation  scheduling.Observation
}

func (u *controlledBridgeUpstream) Do(req *http.Request, _ string, _ int64, _ int) (*http.Response, error) {
	u.called = true
	if dispatch, ok := req.Context().Value(controlledDispatchContextKey{}).(*controlledDispatch); ok {
		u.observation = dispatch.healthObservation
	}
	buffered, _ := req.Context().Value(controlledBufferedResponseContextKey{}).(bool)
	require.Equal(u.t, u.wantBuffered, buffered, "forwarding route must preserve the client's buffering choice")
	if u.server == nil {
		return nil, errors.New("fixture transport stop")
	}
	local := req.Clone(req.Context())
	target, err := url.Parse(u.server.URL)
	require.NoError(u.t, err)
	local.URL, local.Host = target, ""
	return u.server.Client().Do(local)
}

func (u *controlledBridgeUpstream) DoWithTLS(req *http.Request, proxy string, accountID int64, concurrency int, _ *tlsfingerprint.Profile) (*http.Response, error) {
	return u.Do(req, proxy, accountID, concurrency)
}

func controlledBridgeRequest(route string, stream bool) (string, []byte) {
	switch route {
	case "chat", "grokchat", "nativechat", "gatewaychat":
		return "/v1/chat/completions", []byte(fmt.Sprintf(`{"model":"test-model","messages":[{"role":"user","content":"hello"}],"stream":%t}`, stream))
	case "messages":
		return "/v1/messages", []byte(fmt.Sprintf(`{"model":"test-model","max_tokens":32,"messages":[{"role":"user","content":"hello"}],"stream":%t}`, stream))
	default:
		return "/v1/responses", []byte(fmt.Sprintf(`{"model":"test-model","input":"hello","stream":%t}`, stream))
	}
}

func runControlledBridgeRoute(ctx context.Context, svc *OpenAIGatewayService, c *gin.Context, account *Account, route string, body []byte, stream bool) (*OpenAIForwardResult, error) {
	switch route {
	case "chat":
		return svc.ForwardAsChatCompletions(ctx, c, account, body, "", "")
	case "grokchat":
		return svc.forwardGrokChatCompletionsViaResponses(ctx, c, account, body, "fixture-session", "")
	case "nativechat":
		return svc.forwardChatCompletionsViaNativeAnthropic(ctx, c, account, body, "")
	case "nativeresponses":
		return svc.forwardResponsesViaNativeAnthropic(ctx, c, account, body, "")
	case "gatewaychat":
		gateway := &GatewayService{cfg: svc.cfg, httpUpstream: svc.httpUpstream}
		_, err := gateway.ForwardAsChatCompletions(ctx, c, account, body, nil)
		return nil, err
	case "gatewayresponses":
		gateway := &GatewayService{cfg: svc.cfg, httpUpstream: svc.httpUpstream}
		_, err := gateway.ForwardAsResponses(ctx, c, account, body, nil)
		return nil, err
	case "messages":
		return svc.ForwardAsAnthropic(ctx, c, account, body, "", "")
	case "passthrough":
		return svc.forwardOpenAIPassthrough(ctx, c, account, body, body, "test-model", false, nil, stream, time.Now())
	case "grok":
		return svc.forwardGrokResponses(ctx, c, account, body, "test-model", stream, time.Now())
	default:
		return svc.Forward(ctx, c, account, body)
	}
}

func controlledBridgeAccount(account *Account, route string) {
	account.Type = AccountTypeAPIKey
	account.Credentials = map[string]any{"api_key": "fixture", "base_url": "https://api.openai.com"}
	account.Extra = map[string]any{"openai_passthrough": false, "openai_responses_supported": true}
	if route == "grok" || route == "grokchat" {
		account.Platform = PlatformGrok
	}
	if route == "grokchat" {
		account.Credentials["model_mapping"] = map[string]any{"test-model": "grok-4.6"}
	}
	if strings.HasPrefix(route, "native") {
		account.Platform = PlatformKimi
		account.Credentials["api_protocol"] = APIProtocolAnthropic
	}
	if strings.HasPrefix(route, "gateway") {
		account.Platform = PlatformAnthropic
	}
}

func TestControlledBufferedRouteMarkers(t *testing.T) {
	for _, route := range []string{"chat", "messages", "responses", "passthrough", "grok", "grokchat", "nativechat", "nativeresponses", "gatewaychat", "gatewayresponses"} {
		for _, stream := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/stream_%t", route, stream), func(t *testing.T) {
				account := rawChatCompletionsTestAccount()
				controlledBridgeAccount(account, route)
				upstream := &controlledBridgeUpstream{t: t, wantBuffered: !stream}
				svc := &OpenAIGatewayService{cfg: rawChatCompletionsTestConfig(), httpUpstream: upstream}
				path, body := controlledBridgeRequest(route, stream)
				c, _ := gin.CreateTestContext(httptest.NewRecorder())
				c.Request = httptest.NewRequest(http.MethodPost, path, strings.NewReader(string(body)))
				c.Request.Header.Set("Content-Type", "application/json")
				c.Set("api_key", &APIKey{ID: 7101})
				_, err := runControlledBridgeRoute(context.Background(), svc, c, account, route, body, stream)
				require.Error(t, err)
				require.True(t, upstream.called)
			})
		}
	}
}

const controlledAnthropicStart = "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_1\",\"type\":\"message\",\"role\":\"assistant\",\"content\":[{\"type\":\"text\",\"text\":\"ok\"}],\"usage\":{\"input_tokens\":1}}}\n\n"
const controlledAnthropicDelta = "event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":1}}\n\n"
const controlledAnthropicStop = "event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"

func TestControlledBufferedBridgesRealSSE(t *testing.T) {
	for _, route := range []string{"chat", "messages", "responses", "passthrough", "grok", "grokchat", "nativechat", "nativeresponses", "gatewaychat", "gatewayresponses"} {
		for _, healthy := range []bool{true, false} {
			t.Run(fmt.Sprintf("%s/healthy_%t", route, healthy), func(t *testing.T) {
				body := "data: {\"type\":\"response.completed\"}\n\n"
				if healthy {
					body = "data: {\"type\":\"response.completed\",\"response\":" + protocolResponsesBody + "}\n\n"
				}
				if strings.HasPrefix(route, "native") || strings.HasPrefix(route, "gateway") {
					body = controlledAnthropicStop
					if healthy {
						body = controlledAnthropicStart + controlledAnthropicDelta + controlledAnthropicStop
					}
				}
				runControlledBridgeRealSSE(t, route, body, healthy)
			})
		}
	}
}

func TestControlledBufferedAnthropicTerminalValidation(t *testing.T) {
	for _, route := range []string{"nativechat", "nativeresponses", "gatewaychat", "gatewayresponses"} {
		for _, tc := range []struct{ name, body string }{
			{"missing_stop", controlledAnthropicStart + controlledAnthropicDelta},
			{"missing_stop_reason", controlledAnthropicStart + controlledAnthropicStop},
			{"provider_error", controlledAnthropicStart + controlledAnthropicDelta + "event: error\ndata: {\"type\":\"error\",\"error\":{\"type\":\"overloaded_error\",\"message\":\"busy\"}}\n\n" + controlledAnthropicStop},
		} {
			t.Run(route+"/"+tc.name, func(t *testing.T) {
				runControlledBridgeRealSSE(t, route, tc.body, false)
			})
		}
	}
}

type controlledBridgeFailureExpectation struct {
	wantError    bool
	attributable bool
}

func runControlledBridgeRealSSE(t *testing.T, route, upstreamBody string, healthy bool, failure ...controlledBridgeFailureExpectation) *ControlledRequest {
	t.Helper()
	s, db, _, accounts := controlledIntegration(t, true)
	controlledBridgeAccount(accounts[0], route)
	credentials, err := json.Marshal(accounts[0].Credentials)
	require.NoError(t, err)
	extra, err := json.Marshal(accounts[0].Extra)
	require.NoError(t, err)
	_, err = db.Exec("UPDATE accounts SET platform=$1,type=$2,credentials=$3,extra=$4 WHERE id=1", accounts[0].Platform, accounts[0].Type, string(credentials), string(extra))
	require.NoError(t, err)
	ctx, request := controlledIntegrationRequest(t, s)
	gateModel := resolveOpenAIForwardModel(accounts[0], "test-model", "")
	frozen, err := s.Store.FreezeFailureAdmission(ctx, 1, gateModel)
	require.NoError(t, err)
	gateKey := frozen.ModelKey()
	_, err = db.Exec("INSERT INTO scheduling_failure_gates(gate_key,scope,reason,ready_after) VALUES($1,'account_model','fixture',NOW()-INTERVAL '1 second')", gateKey)
	require.NoError(t, err)
	account := controlledPick(t, s, ctx, request, accounts[:1])
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprint(w, upstreamBody)
		w.(http.Flusher).Flush()
		if healthy && (route == "chat" || route == "messages" || route == "grokchat") {
			// These adapters stop at the accepted terminal frame. EOF is
			// deliberately withheld until adapter settlement cancels it.
			<-req.Context().Done()
		}
	}))
	defer server.Close()
	upstream := &controlledBridgeUpstream{t: t, server: server, wantBuffered: true}
	svc := &OpenAIGatewayService{cfg: rawChatCompletionsTestConfig(), httpUpstream: &controlledHTTPUpstream{service: s, base: upstream}}
	path, body := controlledBridgeRequest(route, false)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, path, strings.NewReader(string(body))).WithContext(ctx)
	c.Request.Header.Set("Content-Type", "application/json")
	c.Set("api_key", &APIKey{ID: 7101})
	_, adapterErr := runControlledBridgeRoute(ctx, svc, c, account, route, body, false)
	require.True(t, upstream.called)
	observation := upstream.observation
	require.NotEmpty(t, observation.HealthIdentity)
	key := scheduling.HealthRedisKey(observation.AccountID, observation.Model, observation.Profile, observation.Reasoning, observation.ContextBucket, observation.Transport, observation.HealthIdentity)
	raw, err := s.redis.HGet(ctx, key, "snapshot").Bytes()
	require.NoError(t, err)
	var health scheduling.HealthSnapshot
	require.NoError(t, json.Unmarshal(raw, &health))
	var state, outcome string
	var recovered bool
	require.NoError(t, db.QueryRow("SELECT state,outcome FROM scheduling_attempts WHERE request_id=$1", request.ID).Scan(&state, &outcome))
	require.NoError(t, db.QueryRow("SELECT ready_after IS NULL FROM scheduling_failure_gates WHERE gate_key=$1", gateKey).Scan(&recovered))
	t.Logf("OBSERVED error=%v state=%s outcome=%s good_streak=%d gate_recovered=%v samples=%+v", adapterErr, state, outcome, health.GoodStreak, recovered, health.Samples)
	require.Equal(t, "settled", state)
	if healthy {
		require.NoError(t, adapterErr)
		require.Equal(t, "completed", outcome)
		require.Equal(t, 1, health.GoodStreak)
		require.True(t, recovered)
	} else {
		want := controlledBridgeFailureExpectation{wantError: true, attributable: true}
		if len(failure) > 0 {
			want = failure[0]
		}
		if want.wantError {
			require.Error(t, adapterErr)
		} else {
			require.NoError(t, adapterErr)
		}
		if want.attributable {
			require.Len(t, health.Samples, 1)
			require.True(t, health.Samples[0].AttributableFailure)
		} else {
			require.Empty(t, health.Samples)
		}
		require.NotEqual(t, "completed", outcome)
		require.Zero(t, health.GoodStreak)
		require.False(t, recovered)
	}
	for _, sample := range health.Samples {
		require.False(t, sample.HasSemanticOutput)
		require.False(t, sample.Timeout)
		require.Equal(t, healthy, sample.Completed)
	}
	return request
}

func TestControlledBufferedResponseFailuresRealSSE(t *testing.T) {
	for _, route := range []string{"chat", "messages", "responses", "passthrough", "grok", "grokchat"} {
		for _, tc := range []struct {
			name, body string
			expected   controlledBridgeFailureExpectation
		}{
			{"server_error", "data: {\"type\":\"response.failed\",\"response\":{\"id\":\"resp_1\",\"status\":\"failed\",\"output\":[],\"error\":{\"code\":\"server_error\",\"message\":\"provider unavailable\"}}}\n\n", controlledBridgeFailureExpectation{true, true}},
			{"cyber_policy", "data: {\"type\":\"response.failed\",\"response\":{\"id\":\"resp_1\",\"status\":\"failed\",\"output\":[],\"error\":{\"code\":\"cyber_policy\",\"message\":\"flagged for cyber policy\"}}}\n\n", controlledBridgeFailureExpectation{true, false}},
			{"incomplete", "data: {\"type\":\"response.incomplete\",\"response\":{\"id\":\"resp_1\",\"status\":\"incomplete\",\"output\":[],\"incomplete_details\":{\"reason\":\"max_output_tokens\"},\"usage\":{\"input_tokens\":1,\"output_tokens\":1}}}\n\n", controlledBridgeFailureExpectation{false, false}},
		} {
			t.Run(route+"/"+tc.name, func(t *testing.T) {
				runControlledBridgeRealSSE(t, route, tc.body, false, tc.expected)
			})
		}
	}
}
