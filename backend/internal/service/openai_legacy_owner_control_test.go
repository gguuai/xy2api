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

type groupOwnerControlRepo struct{ *controlledIntegrationRepo }

func (r groupOwnerControlRepo) ListSchedulableByGroupIDAndPlatform(ctx context.Context, group int64, platform string) ([]Account, error) {
	return r.ListSchedulableByPlatform(ctx, platform)
}
func (r groupOwnerControlRepo) ListSchedulableByPlatform(ctx context.Context, platform string) ([]Account, error) {
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
func (r groupOwnerControlRepo) ListSchedulableUngroupedByPlatform(ctx context.Context, platform string) ([]Account, error) {
	return r.ListSchedulableByPlatform(ctx, platform)
}

func TestControlledGroupPolicyOwnerGuard(t *testing.T) {
	for _, tc := range []struct {
		name, scope, action, session string
		blocked                      bool
	}{
		{"account_pause", scheduling.ScopeAccount, "pause", "old-owner", true},
		{"family_pause", scheduling.ScopeFamily, "pause", "old-owner", false},
		{"account_session_drain", scheduling.ScopeAccount, "session_drain", "old-owner", false},
		{"family_session_drain", scheduling.ScopeFamily, "session_drain", "old-owner", false},
		{"unregistered_session", scheduling.ScopeAccount, "session_drain", "new-owner", false},
		{"running_group_policy_owner", scheduling.ScopeAccount, "", "old-owner", false},
		{"drain_hard_health_stays_blocked", scheduling.ScopeAccount, "session_drain", "old-owner", true},
		{"running_hard_health_owner_unavailable", scheduling.ScopeAccount, "", "old-owner", true},
		{"advanced_movable_pause", scheduling.ScopeAccount, "pause", "old-owner", true},
		{"drain_missing_request_context", scheduling.ScopeAccount, "pause", "old-owner", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			control, db, _, accounts := controlledIntegration(t, false)
			// The production account pool is always enabled. Owner protection must hold
			// under this real group policy, without synthesizing an obsolete legacy mode.
			for _, a := range accounts {
				a.Platform = PlatformOpenAI
				a.Type = AccountTypeAPIKey
			}
			if tc.name == "drain_hard_health_stays_blocked" || tc.name == "running_hard_health_owner_unavailable" {
				until := time.Now().Add(time.Hour)
				accounts[0].RateLimitResetAt = &until
			}
			repo := groupOwnerControlRepo{&controlledIntegrationRepo{db: db, accounts: []*Account{accounts[0], accounts[2]}}}
			cfg := &config.Config{}
			cfg.Gateway.OpenAIWS.Enabled = true
			cfg.Gateway.OpenAIWS.APIKeyEnabled = true
			cfg.Gateway.OpenAIWS.ResponsesWebsocketsV2 = true
			cfg.Gateway.OpenAIWS.StickySessionTTLSeconds = 1800
			cfg.Gateway.OpenAIWS.StickyResponseIDTTLSeconds = 3600
			svc := &OpenAIGatewayService{accountRepo: repo, cache: &schedulerTestGatewayCache{}, cfg: cfg,
				rateLimitService:   newOpenAIAdvancedSchedulerRateLimitService("false"),
				concurrencyService: NewConcurrencyService(schedulerTestConcurrencyCache{}), controlledScheduling: control}
			control.concurrency = svc.concurrencyService
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
				_, e := control.Store.Control(ctx, scheduling.ControlCommand{AccountID: 1, Scope: tc.scope, Action: tc.action, SessionDurationSeconds: 60, SessionMaxTurns: 1})
				require.NoError(t, e)
			}
			if tc.action == "pause" && tc.scope == scheduling.ScopeAccount {
				_, e := db.ExecContext(ctx, "UPDATE accounts SET schedulable=FALSE WHERE id=1")
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
			t.Logf("group account policy scope=%s action=%s selected=%d layer=%s error=%v", tc.scope, tc.action, id, decision.Layer, e)
			bound, be := state.GetResponseAccount(ctx, group, "resp-owned-by-a")
			require.NoError(t, be)
			require.EqualValues(t, 1, bound, "paused or unhealthy strong owner must retain its response binding")
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
				if tc.name == "drain_hard_health_stays_blocked" || tc.name == "running_hard_health_owner_unavailable" {
					require.ErrorIs(t, e, scheduling.ErrNoCandidate, "health restrictions must refuse the original owner without selecting a peer")
				} else {
					require.ErrorIs(t, e, scheduling.ErrControlBlocked)
				}
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
			require.EqualValues(t, 1, id)
			require.True(t, ControlledSchedulingEnabled(ctx), "owner continuation uses the new group account policy")
			require.True(t, controlledRequest(ctx).Policy.AccountPool)
			require.True(t, controlledRequest(ctx).owner)
			require.EqualValues(t, 1, controlledRequest(ctx).ownerAccountID)
			if tc.action == "session_drain" {
				wrong, gateErr := control.beginDispatch(ctx, 3, 10)
				require.Nil(t, wrong)
				require.ErrorIs(t, gateErr, scheduling.ErrControlBlocked, "an existing owner binding cannot authorize another account")
				next, e := control.beginDispatch(ctx, id, 10)
				require.NoError(t, e)
				require.NotNil(t, next)
				next.Finish("not_sent", true, nil)
				_, e = db.ExecContext(ctx, "UPDATE accounts SET schedulable=FALSE WHERE id=1")
				require.NoError(t, e)
				_, e = control.Store.BeginDispatch(ctx, scheduling.DispatchRequest{RequestID: "next-turn", AccountID: 1, SessionID: "old-owner", NodeID: "test"})
				require.ErrorIs(t, e, scheduling.ErrControlBlocked, "a new turn cannot bypass the switch through a retired session grant")
			}
		})
	}
}
