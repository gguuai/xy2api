package service

import (
	"context"
	"strconv"
	"time"
)

// Preserve observed model-quality exclusions without reintroducing the old
// sticky selection ahead of the administrator priority and weight contract.
func (s *OpenAIGatewayService) accountPoolQualitySnapshot(ctx context.Context) (OpenAIQualityState, bool) {
	q := qualityRequest(ctx)
	if q == nil || s.qualityConfig().EffectiveMode() != "enforce" {
		return OpenAIQualityState{}, false
	}
	s.loadQuality(ctx, q)
	q.mu.Lock()
	defer q.mu.Unlock()
	return cloneQualityState(q.state), q.pinned
}

func accountPoolQualityBlocked(a *Account, state OpenAIQualityState, pinned bool, now time.Time) bool {
	if pinned || state.Generation == 0 || a == nil || a.Platform != PlatformOpenAI || a.Type != AccountTypeAPIKey {
		return false
	}
	bad, ok := state.Avoid[strconv.FormatInt(a.ID, 10)]
	return ok && bad.Until > now.UnixMilli()
}

func (s *OpenAIGatewayService) bindAccountPoolQuality(ctx context.Context, state OpenAIQualityState, id int64) {
	q := qualityRequest(ctx)
	if q == nil || state.Generation == 0 || s.qualityConfig().EffectiveMode() != "enforce" {
		return
	}
	q.mu.Lock()
	scope := q.scope
	q.mu.Unlock()
	updated := s.qualityUpdate(scope, OpenAIQualityChange{Kind: "bind", Generation: state.Generation, PreviousBinding: state.Binding, AccountID: id, Now: time.Now().UnixMilli()})
	q.mu.Lock()
	q.state = updated
	q.routeGeneration = state.Generation
	q.mu.Unlock()
}
