//go:build unit

package service

import (
	"context"
	"fmt"
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	"github.com/liulixin-lex/xy2api/internal/config"
	"github.com/liulixin-lex/xy2api/internal/scheduling"
	"github.com/stretchr/testify/require"
)

type legacyOwnerControlRepo struct{ *controlledIntegrationRepo }

func (r legacyOwnerControlRepo) ListSchedulableByGroupIDAndPlatform(ctx context.Context, group int64, platform string) ([]Account, error) {
	return r.ListSchedulableByPlatform(ctx, platform)
}
func (r legacyOwnerControlRepo) ListSchedulableByPlatform(ctx context.Context, platform string) ([]Account, error) {
	var out []Account
	for _, a := range r.accounts {
		v, e := r.GetByID(ctx, a.ID)
		if e != nil {
			return nil, e
		}
		if v.Platform == platform && v.Schedulable {
			out = append(out, *v)
		}
	}
	return out, nil
}
func (r legacyOwnerControlRepo) ListSchedulableUngroupedByPlatform(ctx context.Context, platform string) ([]Account, error) {
	return r.ListSchedulableByPlatform(ctx, platform)
}

func TestControlledLegacyRollbackOwnerGuard(t *testing.T) {
	for _, tc := range []struct {
		name, scope, action, session string
		blocked                      bool
	}{
		{"account_pause", scheduling.ScopeAccount, "pause", "old-owner", true},
		{"family_pause", scheduling.ScopeFamily, "pause", "old-owner", true},
		{"account_session_drain", scheduling.ScopeAccount, "session_drain", "old-owner", false},
		{"family_session_drain", scheduling.ScopeFamily, "session_drain", "old-owner", false},
		{"unregistered_session", scheduling.ScopeAccount, "session_drain", "new-owner", true},
		{"running_legacy_unchanged", scheduling.ScopeAccount, "", "old-owner", false},
		{"drain_hard_health_stays_blocked", scheduling.ScopeAccount, "session_drain", "old-owner", true},
		{"running_hard_health_legacy_fallback", scheduling.ScopeAccount, "", "old-owner", false},
		{"advanced_movable_pause", scheduling.ScopeAccount, "pause", "old-owner", true},
		{"drain_missing_request_context", scheduling.ScopeAccount, "session_drain", "old-owner", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			control, db, p, accounts := controlledIntegration(t, false)
			p.Enabled = false
			_, e := control.Store.PutPolicy(context.Background(), p, p.Version)
			require.NoError(t, e)
			for _, a := range accounts {
				a.Platform = PlatformOpenAI
				a.Type = AccountTypeAPIKey
			}
			if tc.name == "drain_hard_health_stays_blocked" || tc.name == "running_hard_health_legacy_fallback" {
				until := time.Now().Add(time.Hour)
				accounts[0].RateLimitResetAt = &until
			}
			repo := legacyOwnerControlRepo{&controlledIntegrationRepo{db: db, accounts: []*Account{accounts[0], accounts[2]}}}
			cfg := &config.Config{}
			cfg.Gateway.OpenAIWS.Enabled = true
			cfg.Gateway.OpenAIWS.APIKeyEnabled = true
			cfg.Gateway.OpenAIWS.ResponsesWebsocketsV2 = true
			cfg.Gateway.OpenAIWS.StickySessionTTLSeconds = 1800
			cfg.Gateway.OpenAIWS.StickyResponseIDTTLSeconds = 3600
			svc := &OpenAIGatewayService{accountRepo: repo, cache: &schedulerTestGatewayCache{}, cfg: cfg,
				rateLimitService:   newOpenAIAdvancedSchedulerRateLimitService("false"),
				concurrencyService: NewConcurrencyService(schedulerTestConcurrencyCache{}), controlledScheduling: control}
			if tc.name == "advanced_movable_pause" {
				svc.rateLimitService = newOpenAIAdvancedSchedulerRateLimitService("true")
			}
			ctx := NewControlledRequestContext(context.Background(), "responses")
			t.Cleanup(controlledRequest(ctx).Close)
			if tc.name == "drain_missing_request_context" {
				ctx = context.Background()
			}
			group := int64(7)
			state := svc.getOpenAIWSStateStore()
			require.NoError(t, state.BindResponseAccount(ctx, group, "resp-owned-by-a", 1, time.Hour))
			if tc.action == "session_drain" {
				old, e := control.Store.BeginDispatch(ctx, scheduling.DispatchRequest{RequestID: "prior-owner", AccountID: 1, SessionID: "old-owner", NodeID: "test", LeaseDuration: time.Minute})
				require.NoError(t, e)
				require.NoError(t, control.Store.SettleAttempt(ctx, old.TicketID, "completed", false))
			}
			if tc.action != "" {
				_, e = control.Store.Control(ctx, scheduling.ControlCommand{AccountID: 1, Scope: tc.scope, Action: tc.action, SessionDurationSeconds: 60, SessionMaxTurns: 1})
				require.NoError(t, e)
			}
			selection, decision, e := svc.SelectAccountWithScheduler(ctx, &group, "resp-owned-by-a", tc.session, "test-model", nil, OpenAIUpstreamTransportAny, false)
			if tc.name == "advanced_movable_pause" {
				selection, decision, e = svc.selectAccountWithScheduler(ctx, &group, "resp-owned-by-a", tc.session, "test-model", nil, OpenAIUpstreamTransportAny, "", "", false, PlatformOpenAI, true, false)
			}
			if selection != nil && selection.ReleaseFunc != nil {
				defer selection.ReleaseFunc()
			}
			id := int64(0)
			if selection != nil && selection.Account != nil {
				id = selection.Account.ID
			}
			t.Logf("policy.enabled=false scope=%s action=%s selected=%d layer=%s error=%v", tc.scope, tc.action, id, decision.Layer, e)
			bound, be := state.GetResponseAccount(ctx, group, "resp-owned-by-a")
			require.NoError(t, be)
			if tc.name == "running_hard_health_legacy_fallback" {
				require.Zero(t, bound, "ordinary legacy retains its prior binding cleanup")
			} else {
				require.EqualValues(t, 1, bound)
			}
			if tc.blocked {
				var sends atomic.Int64
				if id > 0 {
					response, sendErr := controlledHTTP(t, control, ctx, id, func(w http.ResponseWriter, _ *http.Request) { sends.Add(1); fmt.Fprint(w, "{\"ok\":true}") })
					if response != nil {
						controlledConsume(t, response)
					}
					t.Logf("selected owner substitute=%d actual_local_upstream_sends=%d dispatch_error=%v", id, sends.Load(), sendErr)
				}
				require.Zero(t, sends.Load(), "a paused strong owner must never be bypassed by dispatching B")
				require.ErrorIs(t, e, scheduling.ErrControlBlocked)
				require.Nil(t, selection)
				if controlledRequest(ctx) != nil {
					wrong, gateErr := control.beginDispatch(ctx, 3, 10)
					require.Nil(t, wrong)
					require.ErrorIs(t, gateErr, scheduling.ErrControlBlocked, "a retry with stripped previous_response_id must still obey the original owner")
				}
				return
			}
			require.NoError(t, e)
			require.NotNil(t, selection)
			if tc.name == "running_hard_health_legacy_fallback" {
				require.EqualValues(t, 3, id)
				require.False(t, controlledRequest(ctx).owner)
			} else {
				require.EqualValues(t, 1, id)
			}
			require.False(t, ControlledSchedulingEnabled(ctx), "legacy must remain disabled")
			if tc.action == "session_drain" {
				wrong, gateErr := control.beginDispatch(ctx, 3, 10)
				require.Nil(t, wrong)
				require.ErrorIs(t, gateErr, scheduling.ErrControlBlocked, "a valid drain grant cannot authorize another account")
				next, e := control.beginDispatch(ctx, id, 10)
				require.NoError(t, e)
				require.NotNil(t, next)
				next.Finish("not_sent", true, nil)
				_, e = control.Store.BeginDispatch(ctx, scheduling.DispatchRequest{RequestID: "next-turn", AccountID: 1, SessionID: "old-owner", NodeID: "test"})
				require.ErrorIs(t, e, scheduling.ErrControlBlocked, "selection may not consume the one drain grant, admission must consume exactly once")
			}
		})
	}
}
