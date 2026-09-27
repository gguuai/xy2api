package repository

import (
	"context"
	"errors"
	"github.com/liulixin-lex/xy2api/internal/service"
	"github.com/redis/go-redis/v9"
	"strconv"
)

// Count valid leases without mutating expired members. Keep both lease families
// and the same strict cutoff as GetAccountsLoadBatch's cleanup-and-count path.
func (c *concurrencyCache) PeekAccountsLoadBatch(ctx context.Context, accounts []service.AccountWithConcurrency) (map[int64]*service.AccountLoadInfo, error) {
	result := make(map[int64]*service.AccountLoadInfo, len(accounts))
	if len(accounts) == 0 {
		return result, nil
	}
	now, err := c.rdb.Time(ctx).Result()
	if err != nil {
		return nil, err
	}
	pipe := c.rdb.Pipeline()
	type commands struct {
		account     service.AccountWithConcurrency
		slots, live *redis.IntCmd
		wait        *redis.StringCmd
	}
	all := make([]commands, 0, len(accounts))
	for _, a := range accounts {
		id := strconv.FormatInt(a.ID, 10)
		all = append(all, commands{a, pipe.ZCount(ctx, accountSlotKeyPrefix+id, "("+strconv.FormatInt(now.Unix()-int64(c.slotTTLSeconds), 10), "+inf"), pipe.ZCount(ctx, liveAccountSlotKeyPrefix+id, "("+strconv.FormatInt(now.Unix()-liveLeaseTTLSeconds, 10), "+inf"), pipe.Get(ctx, accountWaitKeyPrefix+id)})
	}
	if _, err = pipe.Exec(ctx); err != nil && !errors.Is(err, redis.Nil) {
		return nil, err
	}
	for _, cmd := range all {
		wait, err := cmd.wait.Int()
		if err != nil && !errors.Is(err, redis.Nil) {
			return nil, err
		}
		used := int(cmd.slots.Val() + cmd.live.Val())
		rate := 0
		if cmd.account.MaxConcurrency > 0 {
			rate = (used + wait) * 100 / cmd.account.MaxConcurrency
		}
		result[cmd.account.ID] = &service.AccountLoadInfo{AccountID: cmd.account.ID, CurrentConcurrency: used, WaitingCount: wait, LoadRate: rate}
	}
	return result, nil
}
