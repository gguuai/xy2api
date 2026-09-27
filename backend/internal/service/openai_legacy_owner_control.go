package service

import (
	"context"
	"fmt"
	"strings"

	"github.com/liulixin-lex/xy2api/internal/scheduling"
)

// selectLegacyControlledOwner keeps administrative controls authoritative when
// the allocation policy is disabled. Uncontrolled legacy traffic keeps its exact
// previous path; only an owner under manual control enters this pinned path.
// The original binding is never removed, and only BeginDispatch spends a grant.
func (s *OpenAIGatewayService) selectLegacyControlledOwner(ctx context.Context, req OpenAIAccountScheduleRequest) (*AccountSelectionResult, OpenAIAccountScheduleDecision, bool, error) {
	decision := OpenAIAccountScheduleDecision{}
	if s == nil || s.controlledScheduling == nil || NormalizeOpenAICompatiblePlatform(req.Platform) != PlatformOpenAI || strings.TrimSpace(req.PreviousResponseID) == "" {
		return nil, decision, false, nil
	}
	state := s.getOpenAIWSStateStore()
	if state == nil {
		return nil, decision, false, nil
	}
	responseID := strings.TrimSpace(req.PreviousResponseID)
	ownerID, err := state.GetResponseAccount(ctx, derefGroupID(req.GroupID), responseID)
	if err != nil {
		return nil, decision, true, fmt.Errorf("%w: continuation owner lookup: %v", scheduling.ErrSharedState, err)
	}
	if ownerID <= 0 {
		return nil, decision, false, nil
	}
	controlled := false
	for _, scope := range []string{scheduling.ScopeFamily, scheduling.ScopeAccount} {
		snapshot, e := s.controlledScheduling.Store.GetControl(ctx, ownerID, scope)
		if e != nil {
			return nil, decision, true, fmt.Errorf("%w: continuation control lookup: %v", scheduling.ErrSharedState, e)
		}
		controlled = controlled || snapshot.State != scheduling.ControlRunning
	}
	if !controlled {
		return nil, decision, false, nil
	}
	// Persist ownership for the whole logical request. An outer legacy retry
	// may remove previous_response_id; final dispatch still cannot change keys.
	r := controlledRequest(ctx)
	if r == nil {
		return nil, decision, true, scheduling.ErrControlBlocked
	}
	r.mu.Lock()
	r.owner = true
	r.ownerAccountID = ownerID
	r.mu.Unlock()
	allowed, err := s.controlledScheduling.Store.CanContinueSession(ctx, ownerID, req.SessionHash)
	if err != nil {
		return nil, decision, true, fmt.Errorf("%w: continuation grant lookup: %v", scheduling.ErrSharedState, err)
	}
	if !allowed {
		return nil, decision, true, scheduling.ErrControlBlocked
	}
	if _, excluded := req.ExcludedIDs[ownerID]; excluded {
		return nil, decision, true, scheduling.ErrControlBlocked
	}

	// Reuse the legacy hard predicates without its sticky-miss fallback or
	// binding deletion. A session grant relaxes only the manual schedulable bit.
	ctx = s.withOpenAIQuotaAutoPauseContext(ctx)
	ctx = s.withOpenAIGroupPrivacyRequirement(ctx, req.GroupID)
	if req.RequiredImageCapability == "" {
		ctx = s.withOpenAIProfitControlGate(ctx, req.GroupID)
	}
	if s.checkChannelPricingRestriction(ctx, req.GroupID, req.RequestedModel) {
		return nil, decision, true, scheduling.ErrControlBlocked
	}
	account, err := s.accountRepo.GetByID(ctx, ownerID)
	if err != nil {
		return nil, decision, true, err
	}
	if account == nil {
		return nil, decision, true, scheduling.ErrControlBlocked
	}
	copy := *account
	copy.Schedulable = true
	account = &copy
	req.Platform = PlatformOpenAI
	req.RequirePrivacySet = s.openAIGroupRequiresPrivacySet(ctx, req.GroupID)
	checker := &defaultOpenAIAccountScheduler{service: s, stats: newOpenAIAccountRuntimeStats()}
	compatible, _ := checker.isAccountRequestCompatibleReason(ctx, account, req)
	if !account.IsOpenAI() || !account.IsSchedulable() || shouldClearStickySession(account, req.RequestedModel) || !s.openAIAccountMatchesSchedulingGroup(account, req.GroupID) || !compatible || !checker.isAccountTransportCompatible(account, req.RequiredTransport) || (req.RequireCompact && openAICompactSupportTier(account) == 0) {
		return nil, decision, true, scheduling.ErrControlBlocked
	}
	if !account.IsOpenAIApiKey() && s.getOpenAIWSProtocolResolver().Resolve(account).Transport != OpenAIUpstreamTransportResponsesWebsocketV2 {
		return nil, decision, true, scheduling.ErrControlBlocked
	}
	acquired, acquireErr := s.tryAcquireAccountSlot(ctx, ownerID, account.Concurrency)
	selection := &AccountSelectionResult{Account: account}
	if acquireErr == nil && acquired != nil && acquired.Acquired {
		selection.Acquired = true
		selection.ReleaseFunc = acquired.ReleaseFunc
	} else if s.concurrencyService != nil {
		cfg := s.schedulingConfig()
		selection.WaitPlan = &AccountWaitPlan{AccountID: ownerID, MaxConcurrency: account.Concurrency, Timeout: cfg.StickySessionWaitTimeout, MaxWaiting: cfg.StickySessionMaxWaiting}
	} else {
		return nil, decision, true, scheduling.ErrControlBlocked
	}
	if req.SessionHash != "" {
		_ = s.bindOpenAIStickySessionDuringSelection(ctx, req.GroupID, req.SessionHash, ownerID)
	}
	decision.Layer = openAIAccountScheduleLayerPreviousResponse
	decision.StickyPreviousHit = true
	decision.SelectedAccountID = ownerID
	decision.SelectedAccountType = account.Type
	return attachSelectionProfitGate(ctx, selection), decision, true, nil
}
