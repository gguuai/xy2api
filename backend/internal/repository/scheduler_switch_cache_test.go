//go:build unit

package repository

import (
	"context"
	"github.com/liulixin-lex/xy2api/internal/service"
	"github.com/stretchr/testify/require"
	"testing"
	"time"
)

func TestOneClickSchedulingReopenInvalidatesMissingMembershipAndStaleWriter(t *testing.T) {
	ctx := context.Background()
	cache := newSchedulerCacheUnit(t)
	a := service.Account{ID: 1, Platform: service.PlatformOpenAI, Type: service.AccountTypeAPIKey, Status: service.StatusActive, Schedulable: true, GroupIDs: []int64{7}}
	b := a
	b.ID = 2
	bucket := service.SchedulerBucket{GroupID: 7, Platform: service.PlatformOpenAI, Mode: service.SchedulerModeSingle}
	token, err := cache.CaptureBucketWriteToken(ctx, bucket)
	require.NoError(t, err)
	require.NoError(t, cache.SetSnapshot(ctx, bucket, token, []service.Account{b}))
	require.NoError(t, cache.SetAccount(ctx, &a))
	got, hit, err := cache.GetSnapshot(ctx, bucket)
	require.NoError(t, err)
	require.True(t, hit)
	require.Len(t, got, 1)
	require.NoError(t, cache.InvalidateAccountScheduling(ctx, []*service.Account{&a}))
	require.Equal(t, time.Duration(snapshotGraceTTLSeconds)*time.Second, cache.rdb.TTL(ctx, schedulerSnapshotKey(bucket, "1")).Val())
	_, hit, err = cache.GetSnapshot(ctx, bucket)
	require.NoError(t, err)
	require.False(t, hit, "reload reopened account from DB on the next request")
	require.ErrorIs(t, cache.SetSnapshot(ctx, bucket, token, []service.Account{b}), service.ErrSchedulerBucketWriteFenced)
	fresh, err := cache.CaptureBucketWriteToken(ctx, bucket)
	require.NoError(t, err)
	require.NoError(t, cache.SetSnapshot(ctx, bucket, fresh, []service.Account{a, b}))
	got, hit, err = cache.GetSnapshot(ctx, bucket)
	require.NoError(t, err)
	require.True(t, hit)
	require.Len(t, got, 2)
	require.NoError(t, cache.RetireBucket(ctx, bucket))
	require.NoError(t, cache.InvalidateAccountScheduling(ctx, []*service.Account{&a}))
	_, err = cache.CaptureBucketWriteToken(ctx, bucket)
	require.ErrorIs(t, err, service.ErrSchedulerBucketRetired, "switch cannot reopen a disabled group")
}
func TestOneClickSchedulingInvalidationKeepsOtherGroupsAndFencesUnpublished(t *testing.T) {
	ctx := context.Background()
	cache := newSchedulerCacheUnit(t)
	account := service.Account{ID: 1, Platform: service.PlatformOpenAI, GroupIDs: []int64{7}, Schedulable: true}
	other := service.SchedulerBucket{GroupID: 8, Platform: service.PlatformOpenAI, Mode: service.SchedulerModeSingle}
	token, err := cache.CaptureBucketWriteToken(ctx, other)
	require.NoError(t, err)
	require.NoError(t, cache.SetSnapshot(ctx, other, token, []service.Account{{ID: 2}}))
	unpublished := service.SchedulerBucket{GroupID: 7, Platform: service.PlatformOpenAI, Mode: service.SchedulerModeForced}
	stale, err := cache.CaptureBucketWriteToken(ctx, unpublished)
	require.NoError(t, err)
	require.NoError(t, cache.InvalidateAccountScheduling(ctx, []*service.Account{&account, &account}))
	_, hit, err := cache.GetSnapshot(ctx, other)
	require.NoError(t, err)
	require.True(t, hit)
	require.ErrorIs(t, cache.SetSnapshot(ctx, unpublished, stale, nil), service.ErrSchedulerBucketWriteFenced)
}
