package scheduling

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

func accountPoolActual(req SelectionRequest, id int64, model string) Observation {
	return Observation{AccountID: id, Model: model, Profile: req.Profile, Reasoning: req.Reasoning, ContextBucket: req.ContextBucket, Transport: req.Transport}
}

func accountPoolPutHealth(t *testing.T, r *Runtime, actual Observation, state HealthSnapshot) {
	t.Helper()
	raw, err := json.Marshal(state)
	require.NoError(t, err)
	require.NoError(t, r.store.client.HSet(context.Background(), HealthRedisKey(actual.AccountID, actual.Model, actual.Profile, actual.Reasoning, actual.ContextBucket, actual.Transport, actual.HealthIdentity), "snapshot", raw).Err())
}

func TestAccountPoolReadmissionMapsActualHealthAndPreservesReceipt(t *testing.T) {
	r, _ := testRuntime(t)
	ctx := context.Background()
	req := accountPoolRequest()
	selected, err := r.Select(ctx, req)
	require.NoError(t, err)
	actual := accountPoolActual(req, selected.AccountID, "actual-upstream-model")
	actual.HealthIdentity = "actual-credential"
	d, err := r.ReadmitAccountPoolHealth(ctx, selected, actual, 0)
	require.NoError(t, err)
	require.Equal(t, selected.ReservationID, d.ReservationID)
	require.Equal(t, selected.PoolKey, d.PoolKey)
	require.Equal(t, req.Policy.Model, selected.HealthFence.Model, "input decision was mutated")
	require.Equal(t, actual.Model, d.HealthFence.Model)
	require.Equal(t, actual.HealthIdentity, d.HealthFence.HealthIdentity)
	require.NotEmpty(t, d.HealthFence.Generation)
	require.False(t, d.Probe)
	fence, err := r.FreezeSelectedHealth(ctx, actual.AccountID, actual.Model, actual.Profile, actual.Reasoning, actual.ContextBucket, actual.Transport, actual.HealthIdentity, d)
	require.NoError(t, err)
	require.Equal(t, *d.HealthFence, fence)
	require.NoError(t, r.CommitSelection(ctx, d))
	actual.At, actual.AttemptID, actual.Fence = time.Now(), "actual-failure", &fence
	actual.AttributableFailure = true
	require.NoError(t, r.Observe(ctx, actual))
	state, err := r.store.client.HGet(ctx, HealthRedisKey(actual.AccountID, actual.Model, actual.Profile, "", "", "", actual.HealthIdentity), "snapshot").Result()
	require.NoError(t, err)
	var health HealthSnapshot
	require.NoError(t, json.Unmarshal([]byte(state), &health))
	require.Equal(t, HealthOpen, health.State)
	require.NoError(t, r.ForgetDispatchReceipts(ctx, d, BudgetReservation{}))
}

func TestAccountPoolReadmissionOpenActualModelRefundsOldReceiptAndProbe(t *testing.T) {
	r, m := testRuntime(t)
	ctx := context.Background()
	req := accountPoolRequest()
	old := accountPoolActual(req, 1, req.Policy.Model)
	accountPoolPutHealth(t, r, old, HealthSnapshot{Generation: "old", State: HealthOpen, CooldownUntilMS: time.Now().Add(-time.Second).UnixMilli()})
	selected, err := r.Select(ctx, req)
	require.NoError(t, err)
	require.True(t, selected.Probe)
	oldProbe := strings.Split(selected.ProbeToken, "|")[0]
	require.True(t, m.Exists(oldProbe))
	actual := accountPoolActual(req, selected.AccountID, "actual")
	accountPoolPutHealth(t, r, actual, HealthSnapshot{Generation: "actual", State: HealthOpen, StageRevision: 8, CooldownUntilMS: time.Now().Add(time.Minute).UnixMilli()})
	d, err := r.ReadmitAccountPoolHealth(ctx, selected, actual, 0)
	require.ErrorIs(t, err, ErrHealthSelectionStale)
	require.Zero(t, d.AccountID)
	require.False(t, m.Exists(oldProbe))
	pending, err := r.store.client.HExists(ctx, selected.PoolKey+":reservations", selected.ReservationID).Result()
	require.NoError(t, err)
	require.False(t, pending)
	require.ErrorIs(t, r.CommitSelection(ctx, selected), ErrSharedState)
	again, err := r.Select(ctx, req)
	require.NoError(t, err)
	require.Equal(t, selected.AccountID, again.AccountID, "failed admission consumed a weight turn")
	require.NoError(t, r.ReleaseSelection(ctx, again))
}

func TestAccountPoolReadmissionTransfersProbeToActualModel(t *testing.T) {
	r, m := testRuntime(t)
	ctx := context.Background()
	req := accountPoolRequest()
	for _, model := range []string{req.Policy.Model, "actual"} {
		accountPoolPutHealth(t, r, accountPoolActual(req, 1, model), HealthSnapshot{Generation: model, State: HealthOpen, CooldownUntilMS: time.Now().Add(-time.Second).UnixMilli()})
	}
	selected, err := r.Select(ctx, req)
	require.NoError(t, err)
	oldProbe := strings.Split(selected.ProbeToken, "|")[0]
	actual := accountPoolActual(req, 1, "actual")
	d, err := r.ReadmitAccountPoolHealth(ctx, selected, actual, 0)
	require.NoError(t, err)
	require.True(t, d.Probe)
	require.NotEqual(t, selected.ProbeToken, d.ProbeToken)
	require.False(t, m.Exists(oldProbe))
	newProbe := strings.Split(d.ProbeToken, "|")[0]
	require.True(t, m.Exists(newProbe))
	require.Equal(t, HealthHalfOpen, d.HealthFence.State)
	// The keys must use one cluster hash tag for joint WATCH/EXEC validation.
	healthKey := HealthRedisKey(1, actual.Model, actual.Profile, "", "", "")
	tag := func(key string) string { return strings.Split(strings.Split(key, "{")[1], "}")[0] }
	require.Equal(t, tag(healthKey), tag(newProbe))
	require.NoError(t, r.CommitSelection(ctx, d))
	require.NoError(t, r.ForgetDispatchReceipts(ctx, d, BudgetReservation{}))
	require.NoError(t, r.ReleaseProbe(ctx, d.ProbeToken))
}

func TestAccountPoolReadmissionRequiresVerifiedOwnerForMissingFence(t *testing.T) {
	r, _ := testRuntime(t)
	req := accountPoolRequest()
	ctx := context.Background()
	selected := Decision{AccountID: 1, Priority: 0, Reason: "protocol_owner"}
	actual := accountPoolActual(req, 1, "actual")
	_, err := r.ReadmitAccountPoolHealth(ctx, selected, actual, 0)
	require.ErrorIs(t, err, ErrHealthSelectionStale)
	_, err = r.ReadmitAccountPoolHealth(ctx, selected, actual, 2)
	require.ErrorIs(t, err, ErrHealthIdentity)
	d, err := r.ReadmitAccountPoolHealth(ctx, selected, actual, 1)
	require.NoError(t, err)
	require.NotNil(t, d.HealthFence)
	require.NotEmpty(t, d.HealthFence.Generation)
	require.Empty(t, d.ReservationID, "owner traffic must not create an artificial weighted selection")
	require.False(t, d.Probe)
	_, err = r.FreezeSelectedHealth(ctx, 1, actual.Model, actual.Profile, "", "", "", "", selected)
	require.ErrorIs(t, err, ErrHealthSelectionStale, "ordinary freeze must retain its non-nil fence requirement")
}

func TestAccountPoolReadmissionOwnerHalfOpenExcludesConcurrentProbe(t *testing.T) {
	r1, m := testRuntime(t)
	client := redis.NewClient(&redis.Options{Addr: m.Addr(), MaxRetries: 0})
	t.Cleanup(func() { _ = client.Close() })
	r2 := NewRuntime(NewRedisStore(client))
	req := accountPoolRequest()
	actual := accountPoolActual(req, 1, "actual")
	accountPoolPutHealth(t, r1, actual, HealthSnapshot{Generation: "actual", State: HealthHalfOpen, StageRevision: 3})
	var wg sync.WaitGroup
	successes := make(chan Decision, 20)
	errorsSeen := make(chan error, 20)
	for i := 0; i < 20; i++ {
		r := r1
		if i%2 == 1 {
			r = r2
		}
		wg.Add(1)
		go func(r *Runtime) {
			defer wg.Done()
			d, err := r.ReadmitAccountPoolHealth(context.Background(), Decision{AccountID: 1}, actual, 1)
			if err != nil {
				errorsSeen <- err
				return
			}
			successes <- d
		}(r)
	}
	wg.Wait()
	close(successes)
	close(errorsSeen)
	require.Len(t, successes, 1)
	require.Len(t, errorsSeen, 19)
	for err := range errorsSeen {
		require.ErrorIs(t, err, ErrCapacity)
	}
	d := <-successes
	require.True(t, d.Probe)
	require.NotEmpty(t, d.ProbeToken)
	require.NoError(t, r1.ReleaseSelection(context.Background(), d))
}

type accountPoolBeforeWatchClient struct {
	redis.UniversalClient
	once   sync.Once
	before func()
}

func (c *accountPoolBeforeWatchClient) Watch(ctx context.Context, fn func(*redis.Tx) error, keys ...string) error {
	c.once.Do(c.before)
	return c.UniversalClient.Watch(ctx, fn, keys...)
}

func TestAccountPoolReadmissionRejectsLostProbeAndChangedGeneration(t *testing.T) {
	for _, failure := range []string{"lease lost", "lease replaced", "generation changed", "opened during admission"} {
		t.Run(failure, func(t *testing.T) {
			r, _ := testRuntime(t)
			ctx := context.Background()
			req := accountPoolRequest()
			selected, err := r.Select(ctx, req)
			require.NoError(t, err)
			actual := accountPoolActual(req, selected.AccountID, "actual")
			state := HealthSnapshot{Generation: "actual", State: HealthHalfOpen, StageRevision: 3}
			accountPoolPutHealth(t, r, actual, state)
			base := r.store.client
			probeKey := accountPoolProbePrefix(actual.AccountID, actual.Model, actual.Profile, "", "", "", "") + ":account:1"
			healthKey := HealthRedisKey(actual.AccountID, actual.Model, actual.Profile, "", "", "")
			r.store.client = &accountPoolBeforeWatchClient{UniversalClient: base, before: func() {
				switch failure {
				case "lease lost":
					require.NoError(t, base.Del(ctx, probeKey).Err())
				case "lease replaced":
					require.NoError(t, base.Set(ctx, probeKey, "another-owner", time.Minute).Err())
				default:
					if failure == "generation changed" {
						state.Generation = "replacement"
					} else {
						state.State = HealthOpen
						state.StageRevision++
						state.CooldownUntilMS = time.Now().Add(time.Minute).UnixMilli()
					}
					raw, err := json.Marshal(state)
					require.NoError(t, err)
					require.NoError(t, base.HSet(ctx, healthKey, "snapshot", raw).Err())
				}
			}}
			d, err := r.ReadmitAccountPoolHealth(ctx, selected, actual, 0)
			require.ErrorIs(t, err, ErrHealthSelectionStale)
			require.Zero(t, d.AccountID)
			pending, err := base.HExists(ctx, selected.PoolKey+":reservations", selected.ReservationID).Result()
			require.NoError(t, err)
			require.False(t, pending)
			if failure == "lease replaced" {
				value, err := base.Get(ctx, probeKey).Result()
				require.NoError(t, err)
				require.Equal(t, "another-owner", value)
			} else {
				n, err := base.Exists(ctx, probeKey).Result()
				require.NoError(t, err)
				require.Zero(t, n)
			}
		})
	}
}

func TestAccountPoolSameIdentityReadmissionCannotRefreshStaleFence(t *testing.T) {
	r, _ := testRuntime(t)
	ctx := context.Background()
	req := accountPoolRequest()
	selected, err := r.Select(ctx, req)
	require.NoError(t, err)
	actual := accountPoolActual(req, selected.AccountID, req.Policy.Model)
	accountPoolPutHealth(t, r, actual, HealthSnapshot{Generation: "new-incarnation", State: HealthHealthy})
	_, err = r.ReadmitAccountPoolHealth(ctx, selected, actual, 0)
	require.ErrorIs(t, err, ErrHealthSelectionStale)
}

type accountPoolDelayedReleaseClient struct {
	redis.UniversalClient
	ready chan struct{}
	allow chan struct{}
	once  sync.Once
}

func (c *accountPoolDelayedReleaseClient) Eval(ctx context.Context, script string, keys []string, args ...any) *redis.Cmd {
	if script == finalizeSelectionLua && len(args) > 1 && args[1] == "release" {
		c.once.Do(func() { close(c.ready) })
		select {
		case <-c.allow:
		case <-ctx.Done():
			cmd := redis.NewCmd(ctx)
			cmd.SetErr(ctx.Err())
			return cmd
		}
	}
	return c.UniversalClient.Eval(ctx, script, keys, args...)
}

func TestAccountPoolCancelledReadmissionReturnsBeforeDetachedCleanup(t *testing.T) {
	r, _ := testRuntime(t)
	req := accountPoolRequest()
	base := r.store.client
	selected, err := r.Select(context.Background(), req)
	require.NoError(t, err)
	delayed := &accountPoolDelayedReleaseClient{UniversalClient: base, ready: make(chan struct{}), allow: make(chan struct{})}
	r.store.client = delayed
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	start := time.Now()
	_, err = r.ReadmitAccountPoolHealth(ctx, selected, accountPoolActual(req, selected.AccountID, "actual"), 0)
	require.ErrorIs(t, err, context.Canceled)
	require.Less(t, time.Since(start), 100*time.Millisecond)
	select {
	case <-delayed.ready:
	case <-time.After(time.Second):
		t.Fatal("detached cleanup did not start")
	}
	pending, err := base.HExists(context.Background(), selected.PoolKey+":reservations", selected.ReservationID).Result()
	require.NoError(t, err)
	require.True(t, pending)
	close(delayed.allow)
	require.Eventually(t, func() bool {
		exists, err := base.HExists(context.Background(), selected.PoolKey+":reservations", selected.ReservationID).Result()
		return err == nil && !exists
	}, time.Second, time.Millisecond)
}

func TestAccountPoolDiagnosticsRemainBoundedWithoutAffectingSelection(t *testing.T) {
	req := accountPoolRequest()
	state := HealthSnapshot{State: HealthHealthy}
	for i := 0; i < 25; i++ {
		state = AdvanceHealth(state, Observation{Profile: req.Profile, At: req.Now.Add(time.Duration(i) * time.Millisecond), Completed: true, HasSemanticOutput: true, TTFT: 100 * time.Second})
	}
	require.Len(t, state.Samples, 20)
	require.Equal(t, 20, state.GoodStreak)
	require.Equal(t, HealthHealthy, state.State)
	require.EqualValues(t, 100000, state.Samples[19].TTFTMS)
	ds, err := Evaluate(req, map[int64]HealthSnapshot{1: state})
	require.NoError(t, err)
	require.Equal(t, map[int64]float64{1: .7, 2: .3}, shares(ds))
	excluded := AdvanceHealth(state, Observation{Profile: req.Profile, At: req.Now, Completed: true, Excluded: true})
	require.Equal(t, state, excluded)
	failed := AdvanceHealth(state, Observation{Profile: req.Profile, At: req.Now.Add(time.Second), AttributableFailure: true})
	require.Zero(t, failed.GoodStreak)
	require.True(t, failed.Samples[len(failed.Samples)-1].AttributableFailure)
	require.Equal(t, HealthOpen, failed.State)
}
