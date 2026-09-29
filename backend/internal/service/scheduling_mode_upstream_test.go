//go:build unit

package service

import (
	"context"
	"github.com/liulixin-lex/xy2api/internal/config"
	"github.com/liulixin-lex/xy2api/internal/scheduling"
	"github.com/stretchr/testify/require"
	"testing"
	"time"
)

func TestSub2APIModePreservesUpstreamPriorityAndPreviousResponseSelection(t *testing.T) {
	for _, advanced := range []bool{false, true} {
		name := "priority_load_awareness"
		if advanced {
			name = "advanced_previous_response"
		}
		t.Run(name, func(t *testing.T) {
			selectWithMode := func(explicit bool) (int64, string) {
				resetOpenAIAdvancedSchedulerSettingCacheForTest()
				ctx := context.Background()
				group := int64(1701)
				cfg := &config.Config{}
				cfg.Gateway.Scheduling.LoadBatchEnabled = false
				cfg.Gateway.OpenAIWS.Enabled = true
				cfg.Gateway.OpenAIWS.OAuthEnabled = true
				cfg.Gateway.OpenAIWS.APIKeyEnabled = true
				cfg.Gateway.OpenAIWS.ResponsesWebsocketsV2 = true
				cfg.Gateway.OpenAIWS.StickyResponseIDTTLSeconds = 3600
				accounts := []Account{{ID: 37001, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Status: StatusActive, Schedulable: true, Concurrency: 1, Priority: 5, Extra: map[string]any{"openai_apikey_responses_websockets_v2_enabled": true}}, {ID: 37002, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Status: StatusActive, Schedulable: true, Concurrency: 1, Priority: 0}}
				svc := &OpenAIGatewayService{cfg: cfg, accountRepo: schedulerTestOpenAIAccountRepo{accounts: accounts}, cache: &schedulerTestGatewayCache{}, concurrencyService: NewConcurrencyService(schedulerTestConcurrencyCache{})}
				if advanced {
					svc.rateLimitService = newOpenAIAdvancedSchedulerRateLimitService("true")
				}
				if explicit {
					ctx = NewControlledRequestContext(ctx, "responses")
					r := controlledRequest(ctx)
					r.Mode = scheduling.ModeSnapshot{Mode: scheduling.ModeSub2API, Version: 2}
					r.modeResolved = true
					svc.controlledScheduling = &ControlledSchedulingService{Store: scheduling.NewPostgresStore(nil), modeReader: func(context.Context) (scheduling.ModeSnapshot, error) {
						t.Fatal("request snapshot must not reread mode")
						return scheduling.ModeSnapshot{}, scheduling.ErrSharedState
					}}
				}
				require.NoError(t, svc.getOpenAIWSStateStore().BindResponseAccount(ctx, group, "resp_fixed_owner", 37001, time.Hour))
				selection, decision, err := svc.SelectAccountWithScheduler(ctx, &group, "resp_fixed_owner", "", "gpt-5.1", nil, OpenAIUpstreamTransportAny, false)
				require.NoError(t, err)
				require.NotNil(t, selection)
				if selection.ReleaseFunc != nil {
					defer selection.ReleaseFunc()
				}
				return selection.Account.ID, decision.Layer
			}
			upstreamID, upstreamLayer := selectWithMode(false)
			modeID, modeLayer := selectWithMode(true)
			require.Equal(t, upstreamID, modeID)
			require.Equal(t, upstreamLayer, modeLayer)
			if advanced {
				require.EqualValues(t, 37001, modeID)
				require.Equal(t, openAIAccountScheduleLayerPreviousResponse, modeLayer)
			} else {
				require.EqualValues(t, 37002, modeID)
				require.Equal(t, openAIAccountScheduleLayerLoadBalance, modeLayer)
			}
		})
	}
}
