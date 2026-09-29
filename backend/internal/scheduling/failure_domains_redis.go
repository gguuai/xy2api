package scheduling

import (
	"context"
	"sort"
	"time"

	"github.com/redis/go-redis/v9" //nolint:depguard // explicit scheduling shares the existing Redis authority.
)

// Redis is a bounded competition lease/cache. The PG attempt probe-key index is
// the durable authority and prevents duplicate remote probes after lease loss.
type RedisFailureDomains struct{ Client redis.UniversalClient }

func (s RedisFailureDomains) Acquire(ctx context.Context, t DispatchTicket) error {
	if t.Failure == nil || len(t.Failure.ProbeVersions) == 0 {
		return nil
	}
	if s.Client == nil {
		return ErrSharedState
	}
	keys := make([]string, 0, len(t.Failure.ProbeVersions))
	for k := range t.Failure.ProbeVersions {
		keys = append(keys, "xy2:scheduling:{failure-probes}:"+k)
	}
	sort.Strings(keys)
	result, err := s.Client.Eval(ctx, `for i,k in ipairs(KEYS) do local v=redis.call('GET',k); if v and v~=ARGV[1] then return 0 end end; for i,k in ipairs(KEYS) do redis.call('SET',k,ARGV[1],'PX',ARGV[2]) end; return 1`, keys, t.TicketID, (2 * time.Minute).Milliseconds()).Int()
	if err != nil {
		return ErrSharedState
	}
	if result != 1 {
		return ErrFailureDomainBlocked
	}
	return nil
}
func (s RedisFailureDomains) Release(ctx context.Context, t DispatchTicket) error {
	if t.Failure == nil || len(t.Failure.ProbeVersions) == 0 {
		return nil
	}
	if s.Client == nil {
		return ErrSharedState
	}
	keys := make([]string, 0, len(t.Failure.ProbeVersions))
	for k := range t.Failure.ProbeVersions {
		keys = append(keys, "xy2:scheduling:{failure-probes}:"+k)
	}
	return s.Client.Eval(ctx, `for i,k in ipairs(KEYS) do if redis.call('GET',k)==ARGV[1] then redis.call('DEL',k) end end; return 1`, keys, t.TicketID).Err()
}
