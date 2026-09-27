package scheduling

import (
	"context"
	"encoding/json"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
	"os"
	"testing"
	"time"
)

func runInspectionSequence(t *testing.T, r *Runtime, dump func() string, rules ...AccountRule) {
	t.Helper()
	ctx := context.Background()
	now := time.Now()
	req := SelectionRequest{Policy: Policy{Enabled: true, Model: "explain", Accounts: []AccountRule{{AccountID: 1, Weight: 7}, {AccountID: 2, Weight: 3}}}, Candidates: []Candidate{candidate(1, 0), candidate(2, 0)}, Now: now}
	if len(rules) > 0 {
		req.Policy.Accounts = rules
		req.Candidates = nil
		for _, rule := range rules {
			req.Candidates = append(req.Candidates, candidate(rule.AccountID, 0))
		}
	}
	for i := 0; i < 24; i++ {
		before := dump()
		got, e := r.InspectSelection(ctx, req)
		require.NoError(t, e)
		require.NotNil(t, got.Selected)
		require.Equal(t, before, dump(), "inspection must not write")
		actual, e := r.Select(ctx, req)
		require.NoError(t, e)
		require.Equal(t, actual.AccountID, got.Selected.AccountID, "snapshot must predict current SWRR balance at step %d; %s", i, before)
		if i%4 != 0 {
			require.NoError(t, r.CommitSelection(ctx, actual))
		}
	}
	// Move only the logical allocator clock. Expired reservations stay physically
	// present during inspection, and their compensation is simulated in memory.
	req.Now = now.Add(2 * time.Minute)
	before := dump()
	got, e := r.InspectSelection(ctx, req)
	require.NoError(t, e)
	require.Equal(t, before, dump())
	actual, e := r.Select(ctx, req)
	require.NoError(t, e)
	require.Equal(t, actual.AccountID, got.Selected.AccountID)
	require.NoError(t, r.CommitSelection(ctx, actual))
}
func TestInspectionPreservesStateAndPredictsSWRRIncludingExpiredReservations(t *testing.T) {
	r, m := testRuntime(t)
	runInspectionSequence(t, r, m.Dump)
}
func TestInspectionSkipsBusyProbeWithoutReserving(t *testing.T) {
	r, m := testRuntime(t)
	ctx := context.Background()
	req := SelectionRequest{Policy: Policy{Enabled: true, Model: "probe"}, Profile: profile(), Candidates: []Candidate{candidate(1, 0), candidate(2, 0)}, Now: time.Now()}
	first, e := r.Select(ctx, req)
	require.NoError(t, e)
	before := m.Dump()
	peek, e := r.InspectSelection(ctx, req)
	require.NoError(t, e)
	require.Equal(t, before, m.Dump())
	require.NotEqual(t, first.AccountID, peek.Selected.AccountID)
	next, e := r.Select(ctx, req)
	require.NoError(t, e)
	require.Equal(t, peek.Selected.AccountID, next.AccountID)
	before = m.Dump()
	_, e = r.InspectSelection(ctx, req)
	require.ErrorIs(t, e, ErrCapacity)
	require.Equal(t, before, m.Dump())
}
func TestNextRecoveryDoesNotReviveExcludedAccounts(t *testing.T) {
	r, m := testRuntime(t)
	ctx := context.Background()
	now := time.Now()
	req := SelectionRequest{Policy: Policy{Enabled: true, Model: "recovery", Accounts: []AccountRule{{AccountID: 2, Weight: 0}}}, Profile: profile(), Now: now, Candidates: []Candidate{candidate(1, 0), candidate(2, 0), candidate(3, 0), candidate(4, 0), candidate(5, 0), candidate(6, 0)}, Attempted: map[int64]bool{3: true}, ExcludedFailureDomains: map[string]bool{"provider": true}}
	req.Candidates[0].HardEligible = false
	req.Candidates[3].FailureDomains = []string{"provider"}
	for i, c := range req.Candidates {
		h := HealthSnapshot{State: HealthOpen, UpdatedAtMS: now.UnixMilli(), CooldownUntilMS: now.Add(time.Duration(i+1) * time.Second).UnixMilli()}
		raw, e := json.Marshal(h)
		require.NoError(t, e)
		require.NoError(t, r.store.client.Set(ctx, HealthRedisKey(c.AccountID, req.Policy.Model, req.Profile, "", "", ""), raw, 0).Err())
	}
	before := m.Dump()
	next, e := r.NextRecovery(ctx, req)
	require.NoError(t, e)
	require.Equal(t, now.Add(5*time.Second).UnixMilli(), next.UnixMilli())
	require.Equal(t, before, m.Dump())
	req.Policy.Mode = ModePin
	req.Policy.PinAccountID = 4
	next, e = r.NextRecovery(ctx, req)
	require.NoError(t, e)
	require.True(t, next.IsZero())
}
func TestInspectionRealRedis(t *testing.T) {
	addr := os.Getenv("SCHEDULING_TEST_INSPECT_REDIS_ADDR")
	if addr == "" {
		t.Skip("isolated Redis endpoint not configured")
	}
	client := redis.NewClient(&redis.Options{Addr: addr})
	defer client.Close()
	ctx := context.Background()
	require.NoError(t, client.FlushDB(ctx).Err())
	// The test owns its disposable Redis database. DUMP ignores access metadata.
	snapshot := func() string {
		keys, e := client.Keys(ctx, "*").Result()
		require.NoError(t, e)
		values := map[string]string{}
		for _, k := range keys {
			v, e := client.Dump(ctx, k).Result()
			require.NoError(t, e)
			values[k] = v
		}
		b, e := json.Marshal(values)
		require.NoError(t, e)
		return string(b)
	}
	r := NewRuntime(NewRedisStore(client))
	runInspectionSequence(t, r, snapshot)
	for _, weights := range [][]AccountRule{{{AccountID: 1, Weight: 1}, {AccountID: 2, Weight: 1}, {AccountID: 3, Weight: 1}}, {{AccountID: 1, Weight: 997}, {AccountID: 2, Weight: 13}, {AccountID: 3, Weight: 1}}} {
		runInspectionSequence(t, r, snapshot, weights...)
	}
}

func TestRetryAfterPendingDispatchIsReadOnlyAndAccountsForCredit(t *testing.T) {
	r, m := testRuntime(t)
	ctx := context.Background()
	p := NormalizePolicy(Policy{Model: "credit-preview"})
	key := "xy2:scheduling:{" + digest(scopeKey(p)+":retry_budget") + "}"
	for _, tc := range []struct {
		name                 string
		credit               string
		retry, claimed, want bool
	}{{"retry_needs_two_tokens", "10", true, false, false}, {"two_tokens", "20", true, false, true}, {"initial_earns_one_credit", "9", false, false, true}, {"initial_already_claimed", "9", false, true, false}, {"no_spare_initial_credit", "8", false, false, false}} {
		t.Run(tc.name, func(t *testing.T) {
			require.NoError(t, r.store.client.Set(ctx, key+":credit", tc.credit, 0).Err())
			require.NoError(t, r.store.client.Del(ctx, key+":initial:"+digest("logical")).Err())
			if tc.claimed {
				require.NoError(t, r.store.client.Set(ctx, key+":initial:"+digest("logical"), "previous", 0).Err())
			}
			before := m.Dump()
			got, e := r.CanRetryAfterDispatch(ctx, p, "logical", tc.retry)
			require.NoError(t, e)
			require.Equal(t, tc.want, got)
			require.Equal(t, before, m.Dump())
		})
	}
}
