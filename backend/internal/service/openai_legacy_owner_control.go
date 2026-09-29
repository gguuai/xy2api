package service

import (
	"context"
	"fmt"
	"strings"

	"github.com/liulixin-lex/xy2api/internal/scheduling"
)

// selectLegacyControlledOwner protects a disabled continuation owner even on a
// legacy-compatible call path. It never deletes the owner binding or reroutes a
// continuation that requires provider-side state. Original Sub2API is unchanged.
func (s *OpenAIGatewayService) selectLegacyControlledOwner(ctx context.Context, req OpenAIAccountScheduleRequest) (*AccountSelectionResult, OpenAIAccountScheduleDecision, bool, error) {
	decision := OpenAIAccountScheduleDecision{}
	if s == nil || s.controlledScheduling == nil || Sub2APISchedulingEnabled(ctx) || NormalizeOpenAICompatiblePlatform(req.Platform) != PlatformOpenAI || strings.TrimSpace(req.PreviousResponseID) == "" {
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
	// Preserve an unavailable strong owner even though the retired pause controls
	// no longer govern routing. Independent requests can choose another account;
	// this continuation must not silently replay on a different credential.
	allowed, lookupErr := s.controlledScheduling.Store.CanAdmitControl(ctx, ownerID, req.SessionHash)
	if lookupErr != nil {
		return nil, decision, true, fmt.Errorf("%w: continuation account lookup: %v", scheduling.ErrSharedState, lookupErr)
	}
	if allowed {
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
	return nil, decision, true, scheduling.ErrControlBlocked
}
