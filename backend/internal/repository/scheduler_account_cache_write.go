package repository

import (
	"fmt"

	"github.com/liulixin-lex/xy2api/internal/service"
	"github.com/redis/go-redis/v9"
)

// v2 keys start without pre-versioned payloads. UpdatedAt is strictly monotonic
// per database row (migration 266), including raw SQL and concurrent writers.
// Keep the watermark when deleting payloads so an older refresh stays fenced.
func schedulerAccountRevisionKey(id string) string {
	return "sched:acc:revision:v2:" + id
}

func schedulerAccountRevision(account service.Account) string {
	micros := account.UpdatedAt.UnixMicro()
	if account.UpdatedAt.IsZero() || micros < 0 {
		micros = 0
	}
	// Fixed-width strings avoid Lua floating-point precision limits.
	return fmt.Sprintf("%020d", micros)
}

var writeSchedulerAccountScript = redis.NewScript(
	"local current = redis.call('GET', KEYS[3])\n" +
		"if current ~= false and ARGV[1] < current then return 0 end\n" +
		"redis.call('SET', KEYS[1], ARGV[2])\n" +
		"redis.call('SET', KEYS[2], ARGV[3])\n" +
		"redis.call('SET', KEYS[3], ARGV[1])\nreturn 1\n")
