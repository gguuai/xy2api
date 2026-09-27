package repository

import (
	"context"
	"github.com/alicebob/miniredis/v2"
	"github.com/liulixin-lex/xy2api/internal/service"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
	"testing"
	"time"
)

func TestPeekAccountsLoadBatchPreservesExpiredLeases(t *testing.T) {
	m := miniredis.RunT(t)
	now := time.Now().Truncate(time.Second)
	m.SetTime(now)
	client := redis.NewClient(&redis.Options{Addr: m.Addr()})
	defer client.Close()
	ctx := context.Background()
	cache := NewConcurrencyCache(client, 15, 900).(*concurrencyCache)
	for key, cutoff := range map[string]int64{accountSlotKeyPrefix + "1": now.Unix() - 900, liveAccountSlotKeyPrefix + "1": now.Unix() - 60} {
		require.NoError(t, client.ZAdd(ctx, key, redis.Z{Score: float64(cutoff - 1), Member: "expired"}, redis.Z{Score: float64(cutoff), Member: "boundary"}, redis.Z{Score: float64(cutoff + 1), Member: "active"}).Err())
	}
	require.NoError(t, client.Set(ctx, accountWaitKeyPrefix+"1", 2, 0).Err())
	before := m.Dump()
	got, e := cache.PeekAccountsLoadBatch(ctx, []service.AccountWithConcurrency{{ID: 1, MaxConcurrency: 5}, {ID: 2, MaxConcurrency: 0}})
	require.NoError(t, e)
	require.Equal(t, before, m.Dump())
	require.Equal(t, 2, got[1].CurrentConcurrency)
	require.Equal(t, 2, got[1].WaitingCount)
	require.Zero(t, got[2].CurrentConcurrency)
	actual, e := cache.GetAccountsLoadBatch(ctx, []service.AccountWithConcurrency{{ID: 1, MaxConcurrency: 5}, {ID: 2, MaxConcurrency: 0}})
	require.NoError(t, e)
	require.Equal(t, actual, got)
	require.NotEqual(t, before, m.Dump(), "normal scheduler owns expired-lease cleanup")
}
