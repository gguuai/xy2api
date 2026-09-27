package service

import (
	"context"
	"github.com/liulixin-lex/xy2api/internal/scheduling"
)

// PeekAccountsLoadBatch never trims stale leases or registers any slot.
func (s *ConcurrencyService) PeekAccountsLoadBatch(ctx context.Context, accounts []AccountWithConcurrency) (map[int64]*AccountLoadInfo, error) {
	if len(accounts) == 0 {
		return map[int64]*AccountLoadInfo{}, nil
	}
	if s == nil || s.cache == nil {
		return nil, scheduling.ErrSharedState
	}
	reader, ok := s.cache.(interface {
		PeekAccountsLoadBatch(context.Context, []AccountWithConcurrency) (map[int64]*AccountLoadInfo, error)
	})
	if !ok {
		return nil, scheduling.ErrSharedState
	}
	return reader.PeekAccountsLoadBatch(ctx, accounts)
}
