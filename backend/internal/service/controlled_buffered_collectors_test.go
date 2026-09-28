//go:build unit

package service

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/liulixin-lex/xy2api/internal/config"
	"github.com/liulixin-lex/xy2api/internal/pkg/tlsfingerprint"
	"github.com/liulixin-lex/xy2api/internal/scheduling"
	"github.com/stretchr/testify/require"
)

type controlledCollectorUpstream struct {
	t           *testing.T
	server      *httptest.Server
	observation scheduling.Observation
	buffered    bool
	called      bool
}

func (u *controlledCollectorUpstream) Do(req *http.Request, _ string, _ int64, _ int) (*http.Response, error) {
	u.called = true
	u.buffered, _ = req.Context().Value(controlledBufferedResponseContextKey{}).(bool)
	if dispatch, ok := req.Context().Value(controlledDispatchContextKey{}).(*controlledDispatch); ok {
		u.observation = dispatch.healthObservation
	}
	local := req.Clone(req.Context())
	target, err := url.Parse(u.server.URL)
	require.NoError(u.t, err)
	local.URL, local.Host = target, ""
	return u.server.Client().Do(local)
}

func (u *controlledCollectorUpstream) DoWithTLS(req *http.Request, proxy string, accountID int64, concurrency int, _ *tlsfingerprint.Profile) (*http.Response, error) {
	return u.Do(req, proxy, accountID, concurrency)
}

func controlledCollectorAccount(account *Account, route string) string {
	account.Type = AccountTypeOAuth
	account.Platform = PlatformGemini
	account.Credentials = map[string]any{"access_token": "fixture", "project_id": "fixture-project"}
	if strings.HasPrefix(route, "antigravity") {
		account.Platform = PlatformAntigravity
		account.Credentials["model_mapping"] = map[string]any{"test-model": "gemini-3.1-pro-high"}
		return "gemini-3.1-pro-high"
	}
	return "test-model"
}

func runControlledCollectorRoute(ctx context.Context, c *gin.Context, account *Account, upstream HTTPUpstream, route string, stream bool) (*ForwardResult, error) {
	path := "/v1/messages"
	body := []byte(fmt.Sprintf("{\"model\":\"test-model\",\"max_tokens\":32,\"messages\":[{\"role\":\"user\",\"content\":\"hello\"}],\"stream\":%t}", stream))
	if strings.HasSuffix(route, "chat") {
		path = "/v1/chat/completions"
	}
	if strings.HasSuffix(route, "responses") {
		path = "/v1/responses"
		body = []byte(fmt.Sprintf("{\"model\":\"test-model\",\"input\":\"hello\",\"stream\":%t}", stream))
	}
	if strings.HasSuffix(route, "native") {
		path = "/v1beta/models/test-model:generateContent"
		body = []byte("{\"contents\":[{\"role\":\"user\",\"parts\":[{\"text\":\"hello\"}]}]}")
	}
	c.Request = httptest.NewRequest(http.MethodPost, path, strings.NewReader(string(body))).WithContext(ctx)
	c.Request.Header.Set("Content-Type", "application/json")
	action := "generateContent"
	if stream {
		action = "streamGenerateContent"
	}
	if strings.HasPrefix(route, "antigravity") {
		svc := newAntigravityCompatService(config.GatewayConfig{MaxLineSize: defaultMaxLineSize}, upstream)
		switch route {
		case "antigravity_native":
			return svc.ForwardGemini(ctx, c, account, "test-model", action, stream, body, false)
		case "antigravity_chat":
			return svc.ForwardAsChatCompletions(ctx, c, account, body, nil)
		case "antigravity_responses":
			return svc.ForwardAsResponses(ctx, c, account, body, nil)
		default:
			return svc.Forward(ctx, c, account, body, false)
		}
	}
	svc := &GeminiMessagesCompatService{tokenProvider: &GeminiTokenProvider{}, httpUpstream: upstream, cfg: &config.Config{}}
	switch route {
	case "gemini_native":
		return svc.ForwardNative(ctx, c, account, "test-model", action, stream, body)
	case "gemini_chat":
		return svc.ForwardAsChatCompletions(ctx, c, account, body)
	default:
		return svc.Forward(ctx, c, account, body)
	}
}

func TestControlledBufferedGeminiCollectorsRealSSE(t *testing.T) {
	for _, route := range []string{"antigravity_native", "antigravity_messages", "antigravity_chat", "antigravity_responses", "gemini_native", "gemini_messages", "gemini_chat"} {
		for _, tc := range []struct {
			name, body string
			healthy    bool
		}{
			{"valid", "data: {\"response\":" + protocolGeminiBody + "}\n\ndata: [DONE]\n\n", true},
			{"separate_terminal", "data: {\"response\":{\"candidates\":[{\"content\":{\"parts\":[{\"text\":\"ok\"}]}}]}}\n\ndata: {\"response\":{\"candidates\":[{\"finishReason\":\"STOP\"}],\"usageMetadata\":{\"promptTokenCount\":1,\"candidatesTokenCount\":1}}}\n\ndata: [DONE]\n\n", true},
			{"bare_done", "data: [DONE]\n\n", false},
			{"missing_finish", "data: {\"response\":{\"candidates\":[{\"content\":{\"parts\":[{\"text\":\"partial\"}]}}]}}\n\ndata: [DONE]\n\n", false},
		} {
			t.Run(route+"/"+tc.name, func(t *testing.T) {
				s, db, _, accounts := controlledIntegration(t, true)
				gateModel := controlledCollectorAccount(accounts[0], route)
				credentials, err := json.Marshal(accounts[0].Credentials)
				require.NoError(t, err)
				_, err = db.Exec("UPDATE accounts SET platform=$1,type=$2,credentials=$3 WHERE id=1", accounts[0].Platform, accounts[0].Type, string(credentials))
				require.NoError(t, err)
				ctx, request := controlledIntegrationRequest(t, s)
				frozen, err := s.Store.FreezeFailureAdmission(ctx, 1, gateModel)
				require.NoError(t, err)
				gateKey := frozen.ModelKey()
				_, err = db.Exec("INSERT INTO scheduling_failure_gates(gate_key,scope,reason,ready_after) VALUES($1,'account_model','fixture',NOW()-INTERVAL '1 second')", gateKey)
				require.NoError(t, err)
				account := controlledPick(t, s, ctx, request, accounts[:1])
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
					w.Header().Set("Content-Type", "text/event-stream")
					_, _ = fmt.Fprint(w, tc.body)
					if tc.healthy && strings.HasPrefix(route, "gemini") {
						w.(http.Flusher).Flush()
						<-req.Context().Done()
					}
				}))
				defer server.Close()
				upstream := &controlledCollectorUpstream{t: t, server: server}
				c, _ := gin.CreateTestContext(httptest.NewRecorder())
				_, adapterErr := runControlledCollectorRoute(ctx, c, account, &controlledHTTPUpstream{service: s, base: upstream}, route, false)
				require.True(t, upstream.called, "actual collector route must reach controlled dispatch: %v", adapterErr)
				o := upstream.observation
				require.NotEmpty(t, o.HealthIdentity)
				key := scheduling.HealthRedisKey(o.AccountID, o.Model, o.Profile, o.Reasoning, o.ContextBucket, o.Transport, o.HealthIdentity)
				raw, err := s.redis.HGet(ctx, key, "snapshot").Bytes()
				require.NoError(t, err)
				var health scheduling.HealthSnapshot
				require.NoError(t, json.Unmarshal(raw, &health))
				var state, outcome string
				var recovered bool
				require.NoError(t, db.QueryRow("SELECT state,outcome FROM scheduling_attempts WHERE request_id=$1", request.ID).Scan(&state, &outcome))
				require.NoError(t, db.QueryRow("SELECT ready_after IS NULL FROM scheduling_failure_gates WHERE gate_key=$1", gateKey).Scan(&recovered))
				t.Logf("OBSERVED marker=%v error=%v state=%s outcome=%s good_streak=%d recovered=%v samples=%+v", upstream.buffered, adapterErr, state, outcome, health.GoodStreak, recovered, health.Samples)
				require.Equal(t, "settled", state)
				if tc.healthy {
					require.NoError(t, adapterErr)
					require.Equal(t, "completed", outcome)
					require.Equal(t, 1, health.GoodStreak)
					require.True(t, recovered)
				} else {
					require.Error(t, adapterErr)
					require.NotEqual(t, "completed", outcome)
					require.Zero(t, health.GoodStreak)
					require.False(t, recovered)
				}
				require.True(t, upstream.buffered)
				for _, sample := range health.Samples {
					require.False(t, sample.HasSemanticOutput)
					require.False(t, sample.Timeout)
					require.Equal(t, tc.healthy, sample.Completed)
				}
			})
		}
	}
}

func TestControlledBufferedGeminiCollectorRouteMarkers(t *testing.T) {
	for _, route := range []string{"antigravity_native", "antigravity_messages", "antigravity_chat", "antigravity_responses", "gemini_native", "gemini_messages", "gemini_chat"} {
		for _, stream := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/stream_%t", route, stream), func(t *testing.T) {
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
					w.Header().Set("Content-Type", "text/event-stream")
					_, _ = fmt.Fprint(w, "data: {\"response\":"+protocolGeminiBody+"}\n\ndata: [DONE]\n\n")
				}))
				defer server.Close()
				account := rawChatCompletionsTestAccount()
				controlledCollectorAccount(account, route)
				upstream := &controlledCollectorUpstream{t: t, server: server}
				c, _ := gin.CreateTestContext(httptest.NewRecorder())
				_, err := runControlledCollectorRoute(context.Background(), c, account, upstream, route, stream)
				require.NoError(t, err)
				require.True(t, upstream.called)
				require.Equal(t, !stream, upstream.buffered)
			})
		}
	}
}

func TestControlledBufferedGeminiCollectionDisabled(t *testing.T) {
	b := &controlledResponseBody{buffered: true, success: true, dispatch: &controlledDispatch{request: &ControlledRequest{Policy: scheduling.Policy{Enabled: false}}}}
	err := validateControlledGeminiCollection(&http.Response{Body: b}, map[string]any{}, nil)
	require.NoError(t, err)
	require.True(t, b.nonstreamValidated)
}
