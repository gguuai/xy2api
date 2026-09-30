//go:build unit

package service

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/liulixin-lex/xy2api/internal/config"
	"github.com/liulixin-lex/xy2api/internal/scheduling"
	"github.com/stretchr/testify/require"
)

// Review counterexamples use the production buffered adapter and real stores.
func TestReviewPR74ProtocolEnvelopeCooldown(t *testing.T) {
	for _, tc := range []struct{ name, payload string }{
		{"named_error_control", `{"type":"error","error":{"code":"server_error"}}`},
		{"plain_json_error", `{"error":{"code":"server_error"}}`},
		{"native_response_failed", `{"object":"response","status":"failed","output":[],"error":{"code":"server_error"}}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, db, _, accounts := controlledIntegration(t, false)
			ctx, request := controlledIntegrationRequest(t, s)
			ctx = withControlledBufferedResponse(ctx)
			a := controlledPick(t, s, ctx, request, accounts[:1])
			frozen, err := s.Store.FreezeFailureAdmission(ctx, a.ID, "test-model")
			require.NoError(t, err)
			resp, err := controlledHTTP(t, s, ctx, a.ID, func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = fmt.Fprint(w, tc.payload)
			})
			require.NoError(t, err)
			rec := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(rec)
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil).WithContext(ctx)
			gateway := &OpenAIGatewayService{cfg: &config.Config{}, rateLimitService: &RateLimitService{}}
			_, err = gateway.handleNonStreamingResponse(ctx, resp, c, a, "test-model", "test-model")
			require.Error(t, err)
			require.False(t, c.Writer.Written())
			require.NoError(t, resp.Body.Close())
			var gates int
			require.NoError(t, db.QueryRow("SELECT COUNT(*) FROM scheduling_failure_gates WHERE gate_key=$1 AND ready_after > NOW()", frozen.ModelKey()).Scan(&gates))
			state, err := s.Store.InspectFailureDomains(ctx, a.ID, "test-model")
			require.NoError(t, err)
			key := scheduling.HealthRedisKey(a.ID, frozen.Model, request.Profile, request.Reasoning, controlledBucket(request), request.Protocol, frozen.HealthIdentity)
			removed, err := s.redis.Del(context.Background(), key).Result()
			require.NoError(t, err)
			require.EqualValues(t, 1, removed)
			other := &ControlledSchedulingService{Store: s.Store, Runtime: s.Runtime, redis: s.redis, accounts: s.accounts, node: "review-second-instance"}
			retryCtx, retryRequest := controlledIntegrationRequest(t, other)
			picked, pickErr := other.selectAccount(retryCtx, retryRequest, accounts[:1], func(*Account) (bool, string) { return true, "fixture" }, nil, false)
			var pickedID int64
			if picked != nil {
				pickedID = picked.Account.ID
			}
			t.Logf("OBSERVED envelope=%s pg_cooldowns=%d pg_eligible=%t removed_health_keys=%d after_error=%v after_selected=%d", tc.name, gates, state.Eligible, removed, pickErr, pickedID)
			require.Equal(t, 1, gates, "validated server failure needs the authoritative cooldown")
			require.False(t, state.Eligible)
			require.Error(t, pickErr)
		})
	}
}

func TestReviewPR74BufferedAttemptTimeoutAllowsFailover(t *testing.T) {
	selected := map[string]bool{"responses": true, "raw_chat": true, "native_gemini": true,
		"anthropic": true, "anthropic_passthrough": true, "bedrock": true, "native_anthropic": true,
		"responses_passthrough": true, "images": true, "embeddings": true, "converted_chat": true, "gemini_chat": true, "gemini_messages": true}
	for _, adapter := range protocolAdapters() {
		if !selected[adapter.name] {
			continue
		}
		t.Run(adapter.name, func(t *testing.T) {
			s, _, _, accounts := controlledIntegration(t, true)
			ctx, request := controlledIntegrationRequest(t, s)
			ctx = withControlledBufferedResponse(ctx)
			a := controlledPick(t, s, ctx, request, accounts[:1])
			cancelled := make(chan struct{})
			resp, err := controlledHTTP(t, s, ctx, a.ID, func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusOK)
				w.(http.Flusher).Flush()
				<-r.Context().Done()
				close(cancelled)
			})
			require.NoError(t, err)
			rec := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(rec)
			c.Request = httptest.NewRequest(http.MethodPost, "/test", nil).WithContext(ctx)
			started := time.Now()
			adapterErr := adapter.run(ctx, resp, c, a)
			_ = resp.Body.Close()
			var failover *UpstreamFailoverError
			typed := errors.As(adapterErr, &failover)
			t.Logf("OBSERVED adapter=%s error=%v typed_failover=%t deadline_cause=%t client_err=%v written=%t attempts=%d elapsed_ms=%d", adapter.name, adapterErr, typed, errors.Is(adapterErr, context.DeadlineExceeded), ctx.Err(), c.Writer.Written(), request.Ledger.Snapshot().Attempts, time.Since(started).Milliseconds())
			select {
			case <-cancelled:
			case <-time.After(time.Second):
				t.Fatal("upstream was not cancelled")
			}
			require.NoError(t, ctx.Err(), "caller remains alive with another-attempt budget")
			require.Error(t, adapterErr)
			require.True(t, typed, "attempt timeout before output must allow the handler to select the next account")
			require.ErrorIs(t, adapterErr, context.DeadlineExceeded)
			require.False(t, c.Writer.Written())
			next := controlledPick(t, s, ctx, request, accounts[:2])
			require.EqualValues(t, 2, next.ID)
			healthy, nextErr := controlledHTTP(t, s, ctx, next.ID, func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = fmt.Fprint(w, adapter.body)
			})
			require.NoError(t, nextErr)
			nextErr = adapter.run(ctx, healthy, c, next)
			require.NoError(t, healthy.Body.Close())
			require.NoError(t, nextErr)
			require.Equal(t, 2, request.Ledger.Snapshot().Attempts)
			t.Logf("OBSERVED adapter=%s first_upstream_cancelled=true second_account=%d second_accepted=true attempts=%d", adapter.name, next.ID, request.Ledger.Snapshot().Attempts)
		})
	}
}

func TestReviewPR74AttemptTimeoutCancellationBoundary(t *testing.T) {
	for _, name := range []string{"attempt", "caller_cancelled", "administrator_cancelled", "no_attempt_timeout"} {
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			d := &controlledDispatch{timeout: true, request: &ControlledRequest{clientContext: ctx}}
			switch name {
			case "caller_cancelled":
				cancel()
			case "administrator_cancelled":
				d.adminCancelled.Store(true)
			case "no_attempt_timeout":
				d.timeout = false
			}
			got := d.responseReadError(context.Canceled)
			if name == "attempt" {
				require.ErrorIs(t, got, context.DeadlineExceeded)
				require.NotErrorIs(t, got, context.Canceled)
			} else {
				require.ErrorIs(t, got, context.Canceled)
				require.NotErrorIs(t, got, context.DeadlineExceeded)
			}
		})
	}
}
