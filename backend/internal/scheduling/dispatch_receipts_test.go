package scheduling

import (
	"context"
	"encoding/json"
	"os"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

func runDispatchReceiptChecks(t *testing.T, client *redis.Client) {
	t.Helper()
	ctx := context.Background()
	r := NewRuntime(NewRedisStore(client))
	second := redis.NewClient(client.Options())
	defer func() { require.NoError(t, second.Close()) }()
	other := NewRuntime(NewRedisStore(second))
	req := SelectionRequest{Policy: Policy{GroupID: 487, Model: "receipt-test"}, Profile: profile(), Candidates: []Candidate{candidate(1, 0), candidate(2, 0)}}
	for _, c := range req.Candidates {
		data, e := json.Marshal(healthy(time.Now()))
		require.NoError(t, e)
		require.NoError(t, client.Set(ctx, HealthRedisKey(c.AccountID, req.Policy.Model, req.Profile, "", "", ""), data, time.Hour).Err())
	}
	// Consume one bootstrapped retry token to make initial credit visible.
	warm, e := r.AcquireDispatchBudget(ctx, req.Policy, "warmup", true)
	require.NoError(t, e)
	require.NoError(t, r.CommitDispatchBudget(ctx, warm))
	require.NoError(t, r.ForgetDispatchReceipts(ctx, Decision{}, warm))
	credit := func(b BudgetReservation) int64 {
		v, e := client.Get(ctx, b.PoolKey+":credit").Int64()
		require.NoError(t, e)
		return v
	}
	require.EqualValues(t, 10, credit(warm))

	t.Run("commit both then prepare fails", func(t *testing.T) {
		d, e := r.Select(ctx, req)
		require.NoError(t, e)
		b, e := r.AcquireDispatchBudget(ctx, req.Policy, "aborted-initial", false)
		require.NoError(t, e)
		require.EqualValues(t, 10, credit(b), "reservation must not mint a retry token")
		require.NoError(t, r.CommitSelection(ctx, d))
		require.NoError(t, other.CommitSelection(ctx, d))
		require.NoError(t, r.CommitDispatchBudget(ctx, b))
		require.NoError(t, other.CommitDispatchBudget(ctx, b))
		require.EqualValues(t, 11, credit(b), "duplicate commit must not mint twice")
		require.NoError(t, other.ReleaseSelection(ctx, d))
		require.NoError(t, r.ReleaseSelection(ctx, d))
		require.NoError(t, other.RefundDispatchBudget(ctx, b))
		require.NoError(t, r.RefundDispatchBudget(ctx, b))
		require.EqualValues(t, 10, credit(b))
		again, e := other.Select(ctx, req)
		require.NoError(t, e)
		require.Equal(t, d.AccountID, again.AccountID)
		require.NoError(t, other.ReleaseSelection(ctx, again))
		retryInitial, e := other.AcquireDispatchBudget(ctx, req.Policy, "aborted-initial", false)
		require.NoError(t, e)
		require.NoError(t, other.CommitDispatchBudget(ctx, retryInitial))
		require.EqualValues(t, 11, credit(retryInitial))
		require.NoError(t, other.RefundDispatchBudget(ctx, retryInitial))
		require.EqualValues(t, 10, credit(retryInitial))
	})
	t.Run("Redis commit error after selection is compensated", func(t *testing.T) {
		d, e := r.Select(ctx, req)
		require.NoError(t, e)
		b, e := r.AcquireDispatchBudget(ctx, req.Policy, "redis-failed-prepare", false)
		require.NoError(t, e)
		require.NoError(t, r.CommitSelection(ctx, d))
		// Deliberately corrupt this disposable credit key to obtain a real Redis Lua error.
		require.NoError(t, client.Set(ctx, b.PoolKey+":credit", "not-a-number", time.Minute).Err())
		e = other.CommitDispatchBudget(ctx, b)
		require.ErrorIs(t, e, ErrSharedState)
		t.Logf("observed Redis commit failure: %v", e)
		require.NoError(t, client.Set(ctx, b.PoolKey+":credit", 10, time.Hour).Err())
		require.NoError(t, other.ReleaseSelection(ctx, d))
		require.NoError(t, other.RefundDispatchBudget(ctx, b))
		require.EqualValues(t, 10, credit(b))
		next, e := r.Select(ctx, req)
		require.NoError(t, e)
		require.Equal(t, d.AccountID, next.AccountID)
		require.NoError(t, r.ReleaseSelection(ctx, next))
	})
	t.Run("actual dispatch confirmation cannot later refund", func(t *testing.T) {
		d, e := r.Select(ctx, req)
		require.NoError(t, e)
		b, e := r.AcquireDispatchBudget(ctx, req.Policy, "really-sent", true)
		require.NoError(t, e)
		require.NoError(t, r.CommitSelection(ctx, d))
		require.NoError(t, r.CommitDispatchBudget(ctx, b))
		require.EqualValues(t, 0, credit(b))
		scores, e := client.HGetAll(ctx, d.PoolKey+":scores").Result()
		require.NoError(t, e)
		require.NoError(t, r.ForgetDispatchReceipts(ctx, d, b))
		require.NoError(t, other.ForgetDispatchReceipts(ctx, d, b))
		require.NoError(t, other.ReleaseSelection(ctx, d))
		require.NoError(t, other.RefundDispatchBudget(ctx, b))
		after, e := client.HGetAll(ctx, d.PoolKey+":scores").Result()
		require.NoError(t, e)
		require.Equal(t, scores, after)
		require.EqualValues(t, 0, credit(b))
	})
	t.Run("old signature does not create catch-up debt", func(t *testing.T) {
		d, e := r.Select(ctx, req)
		require.NoError(t, e)
		require.NoError(t, r.CommitSelection(ctx, d))
		changed := req
		changed.Policy.Accounts = []AccountRule{{AccountID: 1, Weight: 7}, {AccountID: 2, Weight: 3}}
		fresh, e := other.Select(ctx, changed)
		require.NoError(t, e)
		scores, e := client.HGetAll(ctx, fresh.PoolKey+":scores").Result()
		require.NoError(t, e)
		require.NoError(t, r.ReleaseSelection(ctx, d))
		after, e := client.HGetAll(ctx, fresh.PoolKey+":scores").Result()
		require.NoError(t, e)
		require.Equal(t, scores, after)
		require.NoError(t, other.ReleaseSelection(ctx, fresh))
	})
	t.Run("expired commit receipt never makes up traffic", func(t *testing.T) {
		d, e := r.Select(ctx, req)
		require.NoError(t, e)
		require.NoError(t, r.CommitSelection(ctx, d))
		scores, e := client.HGetAll(ctx, d.PoolKey+":scores").Result()
		require.NoError(t, e)
		require.NoError(t, client.ZAdd(ctx, d.PoolKey+":receipt_expiry", redis.Z{Score: float64(time.Now().Add(-time.Second).UnixMilli()), Member: d.ReservationID}).Err())
		require.NoError(t, r.ReleaseSelection(ctx, d))
		after, e := client.HGetAll(ctx, d.PoolKey+":scores").Result()
		require.NoError(t, e)
		require.Equal(t, scores, after)
	})
	t.Run("expired budget commit fails closed", func(t *testing.T) {
		b, e := r.AcquireDispatchBudget(ctx, req.Policy, "expired", false)
		require.NoError(t, e)
		require.NoError(t, client.Del(ctx, b.PoolKey+":budget:"+b.ID).Err())
		require.ErrorIs(t, r.CommitDispatchBudget(ctx, b), ErrSharedState)
	})
}

func TestDispatchReceiptsCompensatePreparedCalls(t *testing.T) {
	r, _ := testRuntime(t)
	client, ok := r.store.client.(*redis.Client)
	require.True(t, ok)
	runDispatchReceiptChecks(t, client)
}
func TestDispatchReceiptsRealRedis(t *testing.T) {
	addr := os.Getenv("SCHEDULING_TEST_RECEIPT_REDIS_ADDR")
	if addr == "" {
		t.Skip("isolated receipt Redis endpoint not configured")
	}
	client := redis.NewClient(&redis.Options{Addr: addr, MaxRetries: -1})
	defer func() { require.NoError(t, client.Close()) }()
	require.NoError(t, client.FlushDB(context.Background()).Err())
	t.Logf("running independent workers against isolated Redis %s", addr)
	runDispatchReceiptChecks(t, client)
}

func TestLatencyProfileTransportAliasesStayDistinct(t *testing.T) {
	pairs := [][2]string{{"anthropic", "messages"}, {"chat_completions", "chat"}, {"response", "responses"}, {"websocket", "ws"}, {"gemini", "gemini"}, {"http", "http"}}
	for _, pair := range pairs {
		t.Run(pair[0], func(t *testing.T) {
			p := Policy{Profiles: []LatencyProfile{{Name: "alias", Transport: pair[0]}}}
			got, ok := ResolveProfileForTransport(p, "", 0, pair[1])
			require.True(t, ok)
			require.Equal(t, pair[1], got.Transport)
			got, ok = ResolveProfileForTransport(p, "", 0, pair[0])
			require.True(t, ok)
			require.Equal(t, pair[1], got.Transport)
			for _, other := range pairs {
				if pair[1] != other[1] {
					_, ok = ResolveProfileForTransport(p, "", 0, other[1])
					require.False(t, ok, "must not merge %s and %s", pair[1], other[1])
				}
			}
		})
	}
}
