//go:build unit

package service

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/liulixin-lex/xy2api/internal/config"
	"github.com/liulixin-lex/xy2api/internal/scheduling"
	"github.com/stretchr/testify/require"
)

// The scheduler snapshot can contain a larger pool than the current group,
// especially in simple mode. Real selection must still apply group membership.
type accountPoolEntryRepo struct{ *controlledIntegrationRepo }

func (r accountPoolEntryRepo) ListSchedulableByGroupIDAndPlatform(ctx context.Context, _ int64, _ string) ([]Account, error) {
	return r.ListByGroup(ctx, 0)
}

func (r accountPoolEntryRepo) ListSchedulableByPlatform(ctx context.Context, _ string) ([]Account, error) {
	return r.ListByGroup(ctx, 0)
}

func (r accountPoolEntryRepo) ListSchedulableUngroupedByPlatform(ctx context.Context, _ string) ([]Account, error) {
	return r.ListByGroup(ctx, 0)
}

func accountPoolEntryGateway(s *ControlledSchedulingService, mode string) *OpenAIGatewayService {
	repo := accountPoolEntryRepo{s.accounts.(*controlledIntegrationRepo)}
	concurrency := NewConcurrencyService(schedulerTestConcurrencyCache{})
	s.accounts, s.concurrency = repo, concurrency
	return &OpenAIGatewayService{
		accountRepo: repo, controlledScheduling: s, concurrencyService: concurrency,
		cfg:   &config.Config{RunMode: mode},
		cache: &schedulerTestGatewayCache{sessionBindings: map[string]int64{"same-session": 3}},
	}
}

func TestAccountPoolEntryExplicitGroupExcludesOutsideSimplePool(t *testing.T) {
	for _, mode := range []string{config.RunModeStandard, config.RunModeSimple} {
		t.Run(mode, func(t *testing.T) {
			s, db, _, accounts := controlledIntegration(t, false)
			_, err := db.Exec("INSERT INTO groups(id) VALUES(8); INSERT INTO accounts(id,priority) VALUES(4,-100); INSERT INTO account_groups(account_id,group_id) VALUES(4,8)")
			require.NoError(t, err)
			outside := &Account{ID: 4, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Priority: -100, Concurrency: 10, Status: StatusActive, Schedulable: true, GroupIDs: []int64{8}}
			s.accounts.(*controlledIntegrationRepo).accounts = append(accounts, outside)
			gateway := accountPoolEntryGateway(s, mode)
			ctx := NewControlledRequestContext(context.Background(), "responses")
			r := controlledRequest(ctx)
			defer r.Close()
			groupID := int64(7)
			selected, decision, err := gateway.SelectAccountWithSchedulerForCapability(ctx, &groupID, "", "same-session", "gpt-5.1", nil, OpenAIUpstreamTransportAny, OpenAIEndpointCapabilityResponses, false, false, false)
			require.NoError(t, err)
			require.NotNil(t, selected)
			defer selected.ReleaseFunc()
			t.Logf("ENTRY_GROUP mode=%s requested_group=%d selected=%d selected_groups=%v layer=%s", mode, groupID, selected.Account.ID, selected.Account.GroupIDs, decision.Layer)
			require.Contains(t, selected.Account.GroupIDs, groupID, "an explicit group's policy must not admit an account only available in the simple global pool")
			require.EqualValues(t, 1, selected.Account.ID, "group priority and weight must override both outsider priority and legacy sticky account 3")
		})
	}
}

func TestAccountPoolEntrySharedModelWeightsAndIndependentGroups(t *testing.T) {
	s, db, _, accounts := controlledIntegration(t, false)
	_, err := db.Exec("INSERT INTO groups(id) VALUES(8); INSERT INTO account_groups(account_id,group_id) VALUES(1,8),(2,8),(3,8)")
	require.NoError(t, err)
	for _, account := range accounts {
		account.GroupIDs = []int64{7, 8}
	}
	zero, one := 0, 1
	group8 := scheduling.DefaultGroupPolicy(8)
	group8.Accounts = []scheduling.AccountRule{{AccountID: 1, Priority: &zero, Weight: 3}, {AccountID: 2, Priority: &zero, Weight: 7}, {AccountID: 3, Priority: &one, Weight: 1000000}}
	_, err = s.Store.PutGroupPolicy(context.Background(), group8, 0)
	require.NoError(t, err)
	// Account metadata deliberately disagrees with the explicit group rules.
	accounts[0].Priority, accounts[1].Priority = 200, 100
	gateway := accountPoolEntryGateway(s, config.RunModeStandard)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{"id":"fixture-response","status":"completed","output":[{"type":"message","content":[{"type":"output_text","text":"ok"}]}]}`)
	}))
	defer upstream.Close()
	counts := map[int64]map[int64]int{7: {}, 8: {}}
	for i := 0; i < 20; i++ {
		for _, groupID := range []int64{7, 8} {
			model := []string{"gpt-5.1", "gpt-5.2"}[i%2]
			ctx := NewControlledRequestContext(context.Background(), "responses")
			r := controlledRequest(ctx)
			selected, decision, selectErr := gateway.SelectAccountWithSchedulerForCapability(ctx, &groupID, "", "same-session", model, nil, OpenAIUpstreamTransportAny, OpenAIEndpointCapabilityResponses, false, false, false)
			require.NoError(t, selectErr)
			require.Contains(t, decision.Layer, "account_pool:")
			require.NotEqualValues(t, 3, selected.Account.ID, "sticky and large lower-tier weight must not bypass available top tier")
			req, requestErr := http.NewRequestWithContext(ctx, http.MethodPost, upstream.URL, strings.NewReader(fmt.Sprintf(`{"model":%q,"stream":false}`, model)))
			require.NoError(t, requestErr)
			response, requestErr := s.roundTrip(req, selected.Account.ID, 10, upstream.Client().Do)
			require.NoError(t, requestErr)
			controlledConsume(t, response)
			counts[groupID][selected.Account.ID]++
			selected.ReleaseFunc()
			r.Close()
		}
	}
	require.Equal(t, map[int64]int{1: 14, 2: 6}, counts[7])
	require.Equal(t, map[int64]int{1: 6, 2: 14}, counts[8])
	var sent int
	require.NoError(t, db.QueryRow("SELECT COUNT(*) FROM scheduling_attempts WHERE metrics ? 'sent_at'").Scan(&sent))
	require.Equal(t, 40, sent)
	t.Logf("ENTRY_WEIGHT actual_dispatches=%d alternating_models=2 group7=%v group8=%v sticky_ignored=true", sent, counts[7], counts[8])
}

func TestAccountPoolEntrySimpleDefaultScopeStillIncludesAllAccounts(t *testing.T) {
	s, _, _, _ := controlledIntegration(t, false)
	gateway := accountPoolEntryGateway(s, config.RunModeSimple)
	ctx := NewControlledRequestContext(context.Background(), "responses")
	defer controlledRequest(ctx).Close()
	selected, _, err := gateway.SelectAccountWithSchedulerForCapability(ctx, nil, "", "same-session", "gpt-5.1", nil, OpenAIUpstreamTransportAny, OpenAIEndpointCapabilityResponses, false, false, false)
	require.NoError(t, err)
	defer selected.ReleaseFunc()
	require.Contains(t, []int64{1, 2}, selected.Account.ID)
	require.EqualValues(t, 0, controlledRequest(ctx).Policy.GroupID)
	t.Logf("ENTRY_DEFAULT simple_group0_selected=%d grouped_account_allowed=true", selected.Account.ID)
}

func TestAccountPoolEntryOwnerCannotEscapeExplicitGroup(t *testing.T) {
	s, db, _, accounts := controlledIntegration(t, false)
	_, err := db.Exec("DELETE FROM account_groups WHERE account_id=1; INSERT INTO groups(id) VALUES(8); INSERT INTO account_groups(account_id,group_id) VALUES(1,8)")
	require.NoError(t, err)
	accounts[0].GroupIDs = []int64{8}
	gateway := accountPoolEntryGateway(s, config.RunModeSimple)
	groupID := int64(7)
	require.NoError(t, gateway.getOpenAIWSStateStore().BindResponseAccount(context.Background(), groupID, "resp-owner-outside", 1, time.Hour))
	ctx := NewControlledRequestContext(context.Background(), "responses")
	defer controlledRequest(ctx).Close()
	selected, _, err := gateway.SelectAccountWithSchedulerForCapability(ctx, &groupID, "resp-owner-outside", "", "gpt-5.1", nil, OpenAIUpstreamTransportAny, OpenAIEndpointCapabilityResponses, false, false, false)
	if selected != nil && selected.ReleaseFunc != nil {
		selected.ReleaseFunc()
	}
	require.ErrorContains(t, err, "protocol_owner_outside_authorized_group")
	require.Nil(t, selected)
}
