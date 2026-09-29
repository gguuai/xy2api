//go:build unit

package service

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/liulixin-lex/xy2api/internal/config"
	"github.com/liulixin-lex/xy2api/internal/scheduling"
	"github.com/stretchr/testify/require"
)

// Real local HTTP plus production adapters and scheduling stores.
// A complete invalid envelope is a control; truncation must preserve the same
// pre-output ability to retry, while the client's context remains alive.
func TestReviewV021BufferedReadFailureAllowsFailover(t *testing.T) {
	selected := map[string]bool{"raw_chat": true, "responses": true, "anthropic": true, "gemini_messages": true, "native_gemini": true, "images": true,
		"responses_passthrough": true, "anthropic_passthrough": true, "bedrock": true, "native_anthropic": true, "converted_chat": true, "gemini_chat": true, "embeddings": true}
	for _, adapter := range protocolAdapters() {
		if !selected[adapter.name] {
			continue
		}
		for _, truncated := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/truncated_%t", adapter.name, truncated), func(t *testing.T) {
				s, _, _, accounts := controlledIntegration(t, false)
				ctx, request := controlledIntegrationRequest(t, s)
				ctx = withControlledBufferedResponse(ctx)
				a := controlledPick(t, s, ctx, request, accounts[:1])
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
					w.Header().Set("Content-Type", "application/json")
					payload := "<html>invalid upstream completion</html>"
					if truncated {
						payload = adapter.body
						w.Header().Set("Content-Length", strconv.Itoa(len(payload)+128))
					}
					_, _ = fmt.Fprint(w, payload)
				}))
				defer server.Close()
				req, err := http.NewRequestWithContext(ctx, http.MethodPost, server.URL, strings.NewReader("{\"model\":\"test-model\",\"stream\":false}"))
				require.NoError(t, err)
				resp, err := s.roundTrip(req, a.ID, 10, server.Client().Do)
				require.NoError(t, err)
				rec := httptest.NewRecorder()
				c, _ := gin.CreateTestContext(rec)
				c.Request = httptest.NewRequest(http.MethodPost, "/test", nil).WithContext(ctx)
				adapterErr := adapter.run(ctx, resp, c, a)
				_ = resp.Body.Close()
				var failover *UpstreamFailoverError
				canFailover := errors.As(adapterErr, &failover)
				t.Logf("OBSERVED adapter=%s truncated=%t error=%v typed_failover=%t written=%t status=%d bytes=%d client_err=%v attempts=%d", adapter.name, truncated, adapterErr, canFailover, c.Writer.Written(), rec.Code, rec.Body.Len(), ctx.Err(), request.Ledger.Snapshot().Attempts)
				require.Error(t, adapterErr)
				require.NoError(t, ctx.Err())
				if !canFailover {
					t.Error("pre-output upstream read failure must remain a typed failover, not bypass the handler retry branch")
				}
				if c.Writer.Written() {
					t.Error("adapter committed an error before the handler could retry a healthy account")
				}
			})
		}
	}
}

// Ordinary provider failures must retain the account-pool cooldown when only
// the Redis health snapshot disappears; PostgreSQL is the durable gate.
func TestReviewV021ProviderCooldownSurvivesRedisHealthLoss(t *testing.T) {
	for _, mode := range []string{"http503", "sse", "buffered_sse"} {
		t.Run(mode, func(t *testing.T) {
			sse := mode != "http503"
			s, db, _, accounts := controlledIntegration(t, false)
			ctx, request := controlledIntegrationRequest(t, s)
			if mode == "buffered_sse" {
				ctx = withControlledBufferedResponse(ctx)
			}
			a := controlledPick(t, s, ctx, request, accounts[:1])
			frozen, err := s.Store.FreezeFailureAdmission(ctx, a.ID, "test-model")
			require.NoError(t, err)
			resp, err := controlledHTTP(t, s, ctx, a.ID, func(w http.ResponseWriter, _ *http.Request) {
				if sse {
					w.Header().Set("Content-Type", "text/event-stream")
					fmt.Fprint(w, "data: {\"type\":\"response.failed\",\"response\":{\"status\":\"failed\",\"error\":{\"type\":\"server_error\",\"code\":\"server_error\"}}}\n\n")
				} else {
					w.Header().Set("Content-Type", "application/json")
					w.WriteHeader(http.StatusServiceUnavailable)
					fmt.Fprint(w, "{\"error\":{\"code\":\"server_error\"}}")
				}
			})
			require.NoError(t, err)
			if mode == "buffered_sse" {
				rec := httptest.NewRecorder()
				c, _ := gin.CreateTestContext(rec)
				c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil).WithContext(ctx)
				gateway := &OpenAIGatewayService{cfg: &config.Config{}, rateLimitService: &RateLimitService{}}
				_, err = gateway.handleNonStreamingResponse(ctx, resp, c, a, "test-model", "test-model")
				var failover *UpstreamFailoverError
				require.ErrorAs(t, err, &failover)
				require.False(t, c.Writer.Written())
			} else {
				_, err = io.ReadAll(resp.Body)
				require.NoError(t, err)
			}
			require.NoError(t, resp.Body.Close())
			var gates int
			require.NoError(t, db.QueryRow("SELECT COUNT(*) FROM scheduling_failure_gates WHERE gate_key=$1 AND ready_after > NOW()", frozen.ModelKey()).Scan(&gates))
			state, err := s.Store.InspectFailureDomains(ctx, a.ID, "test-model")
			require.NoError(t, err)
			beforeCtx, beforeRequest := controlledIntegrationRequest(t, s)
			_, beforeErr := s.selectAccount(beforeCtx, beforeRequest, accounts[:1], func(*Account) (bool, string) { return true, "fixture" }, nil, false)
			require.Error(t, beforeErr, "Redis health must initially block the failed account in this fixture")
			key := scheduling.HealthRedisKey(a.ID, frozen.Model, request.Profile, request.Reasoning, controlledBucket(request), request.Protocol, frozen.HealthIdentity)
			removed, err := s.redis.Del(context.Background(), key).Result()
			require.NoError(t, err)
			require.EqualValues(t, 1, removed)
			other := &ControlledSchedulingService{Store: s.Store, Runtime: s.Runtime, redis: s.redis, accounts: s.accounts, node: "second-instance"}
			retryCtx, retryRequest := controlledIntegrationRequest(t, other)
			picked, pickErr := other.selectAccount(retryCtx, retryRequest, accounts[:1], func(*Account) (bool, string) { return true, "fixture" }, nil, false)
			pickedID := int64(0)
			if picked != nil {
				pickedID = picked.Account.ID
			}
			t.Logf("OBSERVED sse=%t pg_cooldowns=%d pg_eligible=%t before_error=%v removed_health_keys=%d after_error=%v after_selected=%d", sse, gates, state.Eligible, beforeErr, removed, pickErr, pickedID)
			if gates != 1 || state.Eligible {
				t.Error("recognized server failure lost its authoritative PostgreSQL cooldown")
			}
			if pickErr == nil {
				t.Error("failed account became selectable before cooldown ended after Redis-only state loss")
			}
		})
	}
}

// The scheduler itself proves dial failures were never sent. These attempts
// must not create a pending usage bill that no usage-log callback can settle.
func TestReviewV021ProvenNotSentHasNoPendingUsage(t *testing.T) {
	for _, refused := range []bool{false, true} {
		t.Run(fmt.Sprintf("connection_refused_%t", refused), func(t *testing.T) {
			s, db, _, accounts := controlledIntegration(t, false)
			ctx, request := controlledIntegrationRequest(t, s)
			a := controlledPick(t, s, ctx, request, accounts[:1])
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusBadRequest)
				fmt.Fprint(w, "{\"error\":{\"code\":\"invalid_parameter\"}}")
			}))
			t.Cleanup(server.Close)
			if refused {
				server.Close()
			}
			req, err := http.NewRequestWithContext(ctx, http.MethodPost, server.URL, strings.NewReader("{\"model\":\"test-model\",\"stream\":false}"))
			require.NoError(t, err)
			resp, transportErr := s.roundTrip(req, a.ID, 10, server.Client().Do)
			if refused {
				var dial *net.OpError
				require.ErrorAs(t, transportErr, &dial)
				require.Equal(t, "dial", dial.Op)
				require.Nil(t, resp)
			} else {
				require.NoError(t, transportErr)
				controlledConsume(t, resp)
			}
			var state, outcome, certainty string
			var pending, acknowledged bool
			require.NoError(t, db.QueryRow("SELECT state,outcome,metrics->>'terminal_certainty',usage_pending,usage_acknowledged FROM scheduling_attempts WHERE request_id=$1", request.ID).Scan(&state, &outcome, &certainty, &pending, &acknowledged))
			t.Logf("OBSERVED refused=%t error=%v state=%s outcome=%s certainty=%s usage_pending=%t usage_acknowledged=%t", refused, transportErr, state, outcome, certainty, pending, acknowledged)
			require.Equal(t, "settled", state)
			if refused {
				require.Equal(t, "not_sent", outcome)
				require.Equal(t, "proven_not_sent", certainty)
			}
			if pending {
				t.Error("an attempt proven not sent must not remain in pending billing")
			}
		})
	}
}
