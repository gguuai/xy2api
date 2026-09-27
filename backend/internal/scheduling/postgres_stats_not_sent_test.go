package scheduling

import (
	"context"
	"github.com/stretchr/testify/require"
	"testing"
	"time"
)

func TestPostgresPreparedButNotSentIsExcludedFromActualTraffic(t *testing.T) {
	s, _ := isolatedControlStore(t)
	ctx := context.Background()
	ticket, e := s.BeginDispatch(ctx, testDispatch(1, ""))
	require.NoError(t, e)
	now := time.Now()
	require.NoError(t, s.RecordAttemptMetrics(ctx, ticket.TicketID, map[string]any{"group_id": 7, "model": "m", "dispatch_kind": "ordinary_first", "sent_at": now.Format(time.RFC3339Nano)}))
	require.NoError(t, s.SettleAttempt(ctx, ticket.TicketID, "not_sent", false))
	stats, e := s.DispatchStatistics(ctx, 7, "m", now.Add(-time.Minute), now.Add(time.Minute))
	require.NoError(t, e)
	require.Zero(t, stats.OrdinaryFirstTotal)
	require.Empty(t, stats.Accounts)
}
