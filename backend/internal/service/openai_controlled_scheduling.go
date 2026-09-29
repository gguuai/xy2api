package service

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/liulixin-lex/xy2api/internal/scheduling"
)

func (s *OpenAIGatewayService) selectControlledOpenAI(ctx context.Context, r *ControlledRequest, req OpenAIAccountScheduleRequest) (*AccountSelectionResult, OpenAIAccountScheduleDecision, error) {
	decision := OpenAIAccountScheduleDecision{Layer: "controlled"}
	ctx = s.withOpenAIQuotaAutoPauseContext(ctx)
	ctx = s.withOpenAIGroupPrivacyRequirement(ctx, req.GroupID)
	if req.RequiredImageCapability == "" {
		ctx = s.withOpenAIProfitControlGate(ctx, req.GroupID)
	}
	if s.checkChannelPricingRestriction(ctx, req.GroupID, req.RequestedModel) {
		return nil, decision, fmt.Errorf("%w: channel pricing restriction", ErrNoAvailableAccounts)
	}
	req.Platform = NormalizeOpenAICompatiblePlatform(req.Platform)
	req.RequirePrivacySet = s.openAIGroupRequiresPrivacySet(ctx, req.GroupID)
	checker := &defaultOpenAIAccountScheduler{service: s}
	qualityState, qualityPinned := s.accountPoolQualitySnapshot(ctx)
	ownerID := int64(0)
	previous := strings.TrimSpace(req.PreviousResponseID)
	if previous != "" {
		store := s.getOpenAIWSStateStore()
		if store == nil {
			return nil, decision, fmt.Errorf("protocol_owner_unavailable")
		}
		var err error
		ownerID, err = store.GetResponseAccount(ctx, derefGroupID(req.GroupID), previous)
		if err != nil {
			return nil, decision, fmt.Errorf("protocol_owner_unavailable: %w", err)
		}
		if ownerID <= 0 {
			return nil, decision, fmt.Errorf("protocol_owner_not_found: continuation requires its original owner")
		}
	} else if parent := s.resolveOpenAIGuardianParentAccountID(ctx, req.GroupID); parent > 0 {
		ownerID = parent
	} else if qualityPinned && qualityState.Generation > 0 {
		ownerID = qualityState.Binding
		if ownerID == 0 {
			ownerID = qualityState.Owner
		}
		if ownerID == 0 {
			return nil, decision, fmt.Errorf("protocol_owner_not_found")
		}
	}
	var accounts []*Account
	if ownerID > 0 {
		a, err := s.accountRepo.GetByID(ctx, ownerID)
		if err != nil {
			return nil, decision, err
		}
		if a == nil {
			return nil, decision, fmt.Errorf("protocol_owner_not_found")
		}
		if !s.openAIAccountMatchesSchedulingGroup(a, req.GroupID) {
			return nil, decision, fmt.Errorf("protocol_owner_outside_authorized_group")
		}
		// Freeze the authorized continuation owner before any availability
		// rejection. Removing a response ID in an adapter must not move state.
		r.mu.Lock()
		r.owner = true
		r.ownerAccountID = ownerID
		r.mu.Unlock()
		if !a.Schedulable {
			return nil, decision, scheduling.ErrControlBlocked
		}

		accounts = []*Account{a}
	} else {
		pool, err := s.listSchedulableAccounts(ctx, req.GroupID, req.Platform)
		if err != nil {
			return nil, decision, err
		}
		if req.Platform == PlatformGrok {
			pool = filterGrokTeamModelRateLimitedAccounts(pool, req.RequestedModel, time.Now())
			pool = filterGrokModelQuotaBlockedAccounts(pool, req.RequestedModel, time.Now())
		}
		for i := range pool {
			accounts = append(accounts, &pool[i])
		}
	}
	eligible := func(account *Account) (bool, string) {
		if account == nil {
			return false, "missing"
		}
		a := account

		if accountPoolQualityBlocked(a, qualityState, qualityPinned || ownerID > 0, time.Now()) {
			return false, "observed_model_quality"
		}
		if !a.IsSchedulable() {
			return false, "hard_unavailable"
		}
		if !s.openAIAccountMatchesSchedulingGroup(a, req.GroupID) {
			return false, "unauthorized_group"
		}
		if a.Platform != req.Platform || !a.IsOpenAICompatible() {
			return false, "platform_mismatch"
		}
		if !checker.isAccountTransportCompatible(a, req.RequiredTransport) {
			return false, "transport_mismatch"
		}
		if req.RequireCompact && openAICompactSupportTier(a) == 0 {
			return false, "compact_unsupported"
		}
		if s.isOpenAIAccountBlockedBySchedulingThreshold(ctx, a) {
			return false, "quota_threshold"
		}
		return checker.isAccountRequestCompatibleReason(ctx, a, req)
	}
	selection, err := s.controlledScheduling.selectAccount(ctx, r, accounts, eligible, req.ExcludedIDs, true)
	if err != nil {
		return nil, decision, err
	}
	if selection != nil && selection.Account != nil {
		r.mu.Lock()
		d := r.Decision
		r.mu.Unlock()
		s.bindAccountPoolQuality(ctx, qualityState, selection.Account.ID)
		decision.Layer = "account_pool:" + d.Reason
		decision.SelectedAccountID = selection.Account.ID
		decision.SelectedAccountType = selection.Account.Type
		decision.CandidateCount = len(accounts)
		return attachSelectionProfitGate(ctx, selection), decision, nil
	}
	return nil, decision, scheduling.ErrNoCandidate
}

func (s *OpenAIGatewayService) selectControlledAuxiliaryOpenAI(ctx context.Context, groupID *int64, sessionID, model string, excluded map[int64]struct{}, capability OpenAIEndpointCapability, platform string) (*Account, bool, error) {
	if s.controlledScheduling == nil {
		return nil, false, nil
	}
	r, enabled, err := s.controlledScheduling.loadPolicy(ctx, groupID, model, sessionID)
	if err != nil || !enabled {
		return nil, enabled, err
	}
	r.mu.Lock()
	r.Auxiliary = true
	r.mu.Unlock()
	selection, _, err := s.selectControlledOpenAI(ctx, r, OpenAIAccountScheduleRequest{GroupID: groupID, SessionHash: sessionID, RequestedModel: model, ExcludedIDs: excluded, RequiredCapability: capability, Platform: platform})
	if err != nil {
		return nil, true, err
	}
	return selection.Account, true, nil
}
