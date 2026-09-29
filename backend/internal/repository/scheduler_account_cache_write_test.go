//go:build unit

package repository

import (
	"context"
	"testing"
	"time"

	"github.com/liulixin-lex/xy2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestOneClickSchedulingLateCacheWriterCannotUndoSwitch(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		name := "off"
		if enabled {
			name = "on"
		}
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			cache := newSchedulerCacheUnit(t)
			bucket := service.SchedulerBucket{GroupID: 7, Platform: service.PlatformOpenAI, Mode: service.SchedulerModeSingle}
			stale := service.Account{ID: 1, Platform: service.PlatformOpenAI, Status: service.StatusActive, Schedulable: !enabled, GroupIDs: []int64{7}, UpdatedAt: time.Date(2026, 9, 29, 0, 0, 0, 0, time.UTC)}
			token, err := cache.CaptureBucketWriteToken(ctx, bucket)
			require.NoError(t, err)
			version, err := cache.allocateSnapshotVersion(ctx, bucket, token)
			require.NoError(t, err)
			current := stale
			current.Schedulable = enabled
			current.UpdatedAt = current.UpdatedAt.Add(time.Microsecond)
			require.NoError(t, cache.SetAccount(ctx, &current))
			require.NoError(t, cache.InvalidateAccountScheduling(ctx, []*service.Account{&current}))
			_, err = cache.writeSnapshotVersionAndReturnAccountIDs(ctx, bucket, version, []service.Account{stale})
			require.NoError(t, err)
			require.ErrorIs(t, cache.activateSnapshotVersion(ctx, bucket, token, version), service.ErrSchedulerBucketWriteFenced)
			require.NoError(t, cache.SetAccount(ctx, &stale))
			zero := stale
			zero.UpdatedAt = time.Time{}
			require.NoError(t, cache.SetAccount(ctx, &zero))
			got, err := cache.GetAccount(ctx, 1)
			require.NoError(t, err)
			require.Equal(t, enabled, got.Schedulable)
			require.Equal(t, current.UpdatedAt, got.UpdatedAt)
			raw, err := cache.rdb.Get(ctx, schedulerAccountMetaKey("1")).Result()
			require.NoError(t, err)
			meta, err := decodeCachedAccount(raw)
			require.NoError(t, err)
			require.Equal(t, enabled, meta.Schedulable)
			// Same committed revision may refresh related runtime metadata.
			current.Name = "fresh runtime label"
			require.NoError(t, cache.SetAccount(ctx, &current))
			got, err = cache.GetAccount(ctx, 1)
			require.NoError(t, err)
			require.Equal(t, current.Name, got.Name)
			require.NoError(t, cache.DeleteAccount(ctx, 1))
			require.NoError(t, cache.SetAccount(ctx, &stale))
			got, err = cache.GetAccount(ctx, 1)
			require.NoError(t, err)
			require.Nil(t, got)
		})
	}
}

func TestOneClickSchedulingDoesNotReadUnversionedLegacyAccountCache(t *testing.T) {
	ctx := context.Background()
	cache := newSchedulerCacheUnit(t)
	legacy := service.Account{ID: 1, Status: service.StatusActive, Schedulable: true}
	full, meta, err := marshalSchedulerCacheAccount(legacy)
	require.NoError(t, err)
	require.NoError(t, cache.rdb.Set(ctx, "sched:acc:1", full, 0).Err())
	require.NoError(t, cache.rdb.Set(ctx, "sched:meta:1", meta, 0).Err())
	got, err := cache.GetAccount(ctx, 1)
	require.NoError(t, err)
	require.Nil(t, got)
	legacy.UpdatedAt = time.Now().UTC().Truncate(time.Microsecond)
	legacy.Schedulable = false
	require.NoError(t, cache.SetAccount(ctx, &legacy))
	got, err = cache.GetAccount(ctx, 1)
	require.NoError(t, err)
	require.False(t, got.Schedulable)
}
