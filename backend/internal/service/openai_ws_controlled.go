package service

import (
	"context"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
)

// prepareControlledWSTurn preserves the first turn's retry ledger while giving
// each subsequently accepted turn its own deadline and budget. Session ownership
// remains stable across turns and does not become ordinary weighted traffic.
func (s *OpenAIGatewayService) prepareControlledWSTurn(ctx context.Context, c *gin.Context, body []byte, originalModel, sessionID string, newTurn bool) (context.Context, error) {
	if s == nil || s.controlledScheduling == nil {
		return ctx, nil
	}
	previous := controlledRequest(ctx)
	if previous != nil {
		previous.mu.Lock()
		if previous.SessionID != "" {
			sessionID = previous.SessionID
		}
		previous.mu.Unlock()
	}
	if previous == nil || newTurn {
		ctx = NewControlledRequestContext(ctx, "ws")
	}
	r := controlledRequest(ctx)
	r.mu.Lock()
	loaded := r.policyLoaded
	r.mu.Unlock()
	if !loaded {
		r.metadata(body)
		r.mu.Lock()
		r.Protocol = "ws"
		r.SessionID = sessionID
		r.owner = newTurn || strings.TrimSpace(gjson.GetBytes(body, "previous_response_id").String()) != ""
		r.mu.Unlock()
	}
	if originalModel == "" {
		originalModel = gjson.GetBytes(body, "model").String()
	}
	if originalModel == "" && previous != nil {
		previous.mu.Lock()
		originalModel = previous.Model
		previous.mu.Unlock()
	}
	groupID := getOpenAIGroupIDFromContext(c)
	_, _, err := s.controlledScheduling.loadPolicy(ctx, &groupID, originalModel, sessionID)
	return ctx, err
}

// A pause removes admission, never ownership. A bounded session grant can relax
// only the manual schedulable bit; all other account health checks still apply.
func (s *OpenAIGatewayService) controlledOwnerForContinuation(ctx context.Context, account *Account) (*Account, bool) {
	if account == nil {
		return nil, false
	}
	if account.Schedulable {
		return account, true
	}
	if s == nil || s.controlledScheduling == nil {
		return account, false
	}
	r := controlledRequest(ctx)
	if r == nil {
		return account, false
	}
	r.mu.Lock()
	sessionID := r.SessionID
	r.mu.Unlock()
	allowed, err := s.controlledScheduling.Store.CanContinueSession(ctx, account.ID, sessionID)
	if err != nil || !allowed {
		return account, false
	}
	copy := *account
	copy.Schedulable = true
	return &copy, true
}
