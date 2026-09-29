//go:build unit

package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/liulixin-lex/xy2api/internal/config"
	"github.com/liulixin-lex/xy2api/internal/pkg/openai_compat"
	"github.com/liulixin-lex/xy2api/internal/server/middleware"
	"github.com/liulixin-lex/xy2api/internal/service"
	"github.com/stretchr/testify/require"
)

type reliabilityAuthRepo struct {
	service.APIKeyRepository
	key   *service.APIKey
	calls int
}

func (r *reliabilityAuthRepo) GetByKeyForAuth(_ context.Context, key string) (*service.APIKey, error) {
	r.calls++
	if key != "test-reliability-key" {
		return nil, service.ErrAPIKeyNotFound
	}
	return r.key, nil
}
func (*reliabilityAuthRepo) UpdateLastUsed(context.Context, int64, time.Time) error { return nil }

type reliabilityLocalUpstream struct {
	service.HTTPUpstream
	client *http.Client
	target *url.URL
	mu     sync.Mutex
	hits   []int64
	bodies [][]byte
	cancel context.CancelFunc
}

func (u *reliabilityLocalUpstream) Do(req *http.Request, _ string, id int64, _ int) (*http.Response, error) {
	body, err := io.ReadAll(req.Body)
	if err != nil {
		return nil, err
	}
	u.mu.Lock()
	u.hits = append(u.hits, id)
	u.bodies = append(u.bodies, body)
	u.mu.Unlock()
	local := req.Clone(req.Context())
	target := *u.target
	target.Path = req.URL.Path
	local.URL = &target
	local.Host = u.target.Host
	local.Body = io.NopCloser(bytes.NewReader(body))
	local.Header.Set("X-Test-Account", strconv.FormatInt(id, 10))
	resp, err := u.client.Do(local)
	if u.cancel != nil {
		u.cancel()
	}
	return resp, err
}

// Real authentication middleware and Gin routing feed production HTTP handlers.
// Only repository data and the final upstream address are synthetic; truncated
// Content-Length bodies travel through a real loopback HTTP client/server.
func TestGatewayBufferedReadFailureAuthenticatedFailover(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, endpoint := range []struct{ name, path, request, response string }{
		{"responses", "/v1/responses", "{\"model\":\"gpt-5\",\"input\":\"hello\",\"stream\":false}", "{\"id\":\"resp_healthy\",\"object\":\"response\",\"status\":\"completed\",\"model\":\"gpt-5\",\"output\":[{\"type\":\"message\",\"role\":\"assistant\",\"content\":[{\"type\":\"output_text\",\"text\":\"healthy\"}]}],\"usage\":{\"input_tokens\":1,\"output_tokens\":1}}"},
		{"chat", "/v1/chat/completions", "{\"model\":\"gpt-5\",\"messages\":[{\"role\":\"user\",\"content\":\"hello\"}],\"stream\":false}", "{\"id\":\"chat_healthy\",\"object\":\"chat.completion\",\"model\":\"gpt-5\",\"choices\":[{\"index\":0,\"message\":{\"role\":\"assistant\",\"content\":\"healthy\"},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":1,\"completion_tokens\":1}}"},
		{"images", "/v1/images/generations", "{\"model\":\"gpt-image-1\",\"prompt\":\"hello\",\"size\":\"1024x1024\"}", "{\"created\":1700000000,\"data\":[{\"b64_json\":\"aGVhbHRoeQ==\"}]}"},
	} {
		for _, scenario := range []string{"fallback", "exhausted", "cancelled", "unauthenticated"} {
			t.Run(endpoint.name+"/"+scenario, func(t *testing.T) {
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					w.Header().Set("Content-Type", "application/json")
					body := endpoint.response
					if r.Header.Get("X-Test-Account") == "1" || scenario == "exhausted" {
						w.Header().Set("Content-Length", strconv.Itoa(len(body)+128))
					}
					_, _ = fmt.Fprint(w, body)
				}))
				defer server.Close()
				target, err := url.Parse(server.URL)
				require.NoError(t, err)
				upstream := &reliabilityLocalUpstream{client: server.Client(), target: target}
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				if scenario == "cancelled" {
					upstream.cancel = cancel
				}
				accounts := []service.Account{}
				for i := int64(1); i <= 2; i++ {
					a := service.Account{ID: i, Platform: service.PlatformOpenAI, Type: service.AccountTypeAPIKey, Status: service.StatusActive, Schedulable: true, Priority: int(i), Credentials: map[string]any{"api_key": "fixture"}}
					if endpoint.name == "chat" {
						a.Extra = map[string]any{openai_compat.ExtraKeyResponsesMode: string(openai_compat.ResponsesSupportModeForceChatCompletions)}
					}
					accounts = append(accounts, a)
				}
				cfg := &config.Config{RunMode: config.RunModeSimple}
				cfg.Gateway.MaxAccountSwitches = 2
				billing := service.NewBillingCacheService(nil, nil, nil, nil, nil, nil, cfg, nil)
				defer billing.Stop()
				gateway := service.NewOpenAIGatewayService(openAIImagesFailoverAccountRepo{accounts: accounts}, nil, nil, nil, nil, nil, nil, cfg, nil, nil, service.NewBillingService(cfg, nil), nil, billing, upstream, nil, nil, nil, nil, nil, nil, nil, nil)
				group := int64(7)
				repo := &reliabilityAuthRepo{key: &service.APIKey{ID: 99, Status: service.StatusActive, GroupID: &group, User: &service.User{ID: 100, Status: service.StatusActive}, Group: &service.Group{ID: group, Status: service.StatusActive, Platform: service.PlatformOpenAI, AllowImageGeneration: true}}}
				keys := service.NewAPIKeyService(repo, nil, nil, nil, nil, nil, cfg)
				h := NewOpenAIGatewayHandler(gateway, service.NewConcurrencyService(nil), billing, keys, nil, nil, nil, nil, cfg)
				h.maxAccountSwitches = 2
				router := gin.New()
				router.Use(gin.HandlerFunc(middleware.NewAPIKeyAuthMiddleware(keys, nil, cfg)))
				router.POST("/v1/responses", h.Responses)
				router.POST("/v1/chat/completions", h.ChatCompletions)
				router.POST("/v1/images/generations", h.Images)
				req := httptest.NewRequest(http.MethodPost, endpoint.path, bytes.NewBufferString(endpoint.request)).WithContext(ctx)
				req.Header.Set("Content-Type", "application/json")
				if scenario != "unauthenticated" {
					req.Header.Set("Authorization", "Bearer test-reliability-key")
				}
				rec := httptest.NewRecorder()
				router.ServeHTTP(rec, req)
				upstream.mu.Lock()
				defer upstream.mu.Unlock()
				if scenario == "unauthenticated" {
					require.Equal(t, 401, rec.Code)
					require.Empty(t, upstream.hits)
					return
				}
				require.Equal(t, 1, repo.calls, "failover must not repeat authentication")
				if scenario == "cancelled" {
					require.Equal(t, []int64{1}, upstream.hits)
					return
				}
				require.Equal(t, []int64{1, 2}, upstream.hits, rec.Body.String())
				require.JSONEq(t, string(upstream.bodies[0]), string(upstream.bodies[1]), "failover must preserve the upstream request")
				require.True(t, json.Valid(rec.Body.Bytes()), rec.Body.String())
				if scenario == "exhausted" {
					require.Equal(t, 502, rec.Code, rec.Body.String())
					return
				}
				require.Equal(t, 200, rec.Code, rec.Body.String())
				require.JSONEq(t, endpoint.response, rec.Body.String())
			})
		}
	}
}
