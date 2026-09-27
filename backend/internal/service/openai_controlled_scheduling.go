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
	}
	var accounts []*Account
	allowDrainingOwner := false
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
		if !a.Schedulable {
			allowed, e := s.controlledScheduling.Store.CanContinueSession(ctx, a.ID, r.SessionID)
			if e != nil {
				return nil, decision, e
			}
			if !allowed {
				return nil, decision, scheduling.ErrControlBlocked
			}
			allowDrainingOwner = true
		}
		accounts = []*Account{a}
		r.mu.Lock()
		r.owner = true
		r.ownerAccountID = ownerID
		r.mu.Unlock()
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
		if allowDrainingOwner && a.ID == ownerID && !a.Schedulable {
			copy := *a
			copy.Schedulable = true
			a = &copy
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
		decision.Layer = "controlled:" + d.Reason
		decision.SelectedAccountID = selection.Account.ID
		decision.SelectedAccountType = selection.Account.Type
		decision.CandidateCount = len(accounts)
		return attachSelectionProfitGate(ctx, selection), decision, nil
	}
	return nil, decision, scheduling.ErrNoCandidate
}
