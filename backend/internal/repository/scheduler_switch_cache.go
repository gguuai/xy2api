package repository

import (
	"context"
	"time"

	"github.com/liulixin-lex/xy2api/internal/pkg/logger"
	"github.com/liulixin-lex/xy2api/internal/service"
	"github.com/redis/go-redis/v9"
)

// Advance the writer epoch without reopening a retired group. Readers miss
// immediately and pre-toggle writers cannot publish stale membership.
var invalidateAccountBucketScript = redis.NewScript(
	"if redis.call('EXISTS', KEYS[2]) == 1 then return 0 end\n" +
		"local generation = tonumber(redis.call('GET', KEYS[1])) or 0\n" +
		"redis.call('SET', KEYS[1], tostring(generation + 1))\n" +
		"local active = redis.call('GET', KEYS[4])\n" +
		"if active ~= false then redis.call('EXPIRE', ARGV[1] .. active, tonumber(ARGV[2])) end\n" +
		"redis.call('DEL', KEYS[3], KEYS[4])\nreturn 1\n")

type accountSchedulingInvalidator interface {
	InvalidateAccountScheduling(context.Context, []*service.Account) error
}

func (c *schedulerCache) InvalidateAccountScheduling(ctx context.Context, accounts []*service.Account) error {
	buckets := service.SchedulerAccountChangeBuckets(accounts)
	if len(buckets) == 0 {
		return nil
	}
	pipe := c.rdb.Pipeline()
	for _, bucket := range buckets {
		keys := []string{schedulerBucketKey(schedulerEpochPrefix, bucket), schedulerBucketKey(schedulerRetiredPrefix, bucket), schedulerBucketKey(schedulerReadyPrefix, bucket), schedulerBucketKey(schedulerActivePrefix, bucket)}
		invalidateAccountBucketScript.Eval(ctx, pipe, keys, schedulerSnapshotPrefix+bucket.String()+":v", snapshotGraceTTLSeconds)
	}
	_, err := pipe.Exec(ctx)
	return err
}

// Explicit switches invalidate membership; ordinary usage/health updates keep
// the inexpensive SetAccount path. Existing outbox events provide repair retries.
func (r *accountRepository) syncSchedulerSwitchDetached(ctx context.Context, ids []int64) {
	if r == nil || r.schedulerCache == nil || len(ids) == 0 {
		return
	}
	base := context.Background()
	if ctx != nil {
		base = context.WithoutCancel(ctx)
	}
	syncCtx, cancel := context.WithTimeout(base, 2*time.Second)
	defer cancel()
	accounts, err := r.GetByIDs(syncCtx, ids)
	if err != nil {
		logger.LegacyPrintf("repository.account", "[Scheduler] switch snapshot read failed: %v", err)
		return
	}
	for _, account := range accounts {
		if account == nil {
			continue
		}
		if err = r.schedulerCache.SetAccount(syncCtx, account); err != nil {
			logger.LegacyPrintf("repository.account", "[Scheduler] switch snapshot write failed: id=%d err=%v", account.ID, err)
		}
	}
	if invalidator, ok := r.schedulerCache.(accountSchedulingInvalidator); ok {
		if err = invalidator.InvalidateAccountScheduling(syncCtx, accounts); err != nil {
			logger.LegacyPrintf("repository.account", "[Scheduler] switch membership invalidation failed: %v", err)
		}
	}
}
