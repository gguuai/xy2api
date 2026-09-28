package scheduling

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

func terminalForHealth(t *testing.T, rt *Runtime, model string, p LatencyProfile, id string) Observation {
	t.Helper()
	fence, err := rt.FreezeHealth(context.Background(), 1, model, p, "default", "unknown", "http")
	require.NoError(t, err)
	return Observation{AttemptID: id, Fence: &fence, AccountID: 1, Model: model, Profile: p, Reasoning: "default", ContextBucket: "unknown", Transport: "http", At: time.Now(), Completed: true}
}

func healthForTest(t *testing.T, rt *Runtime, model string, p LatencyProfile) HealthSnapshot {
	t.Helper()
	states, err := rt.store.Snapshots(context.Background(), SelectionRequest{Policy: Policy{Model: model}, Profile: p, Reasoning: "default", ContextBucket: "unknown", Transport: "http", Candidates: []Candidate{candidate(1, 0)}})
	require.NoError(t, err)
	return states[1]
}

func TestHealthObservationRequiresFrozenAttemptIdentity(t *testing.T) {
	rt, _ := testRuntime(t)
	o := terminalForHealth(t, rt, "m", profile(), "attempt")
	invalid := o
	invalid.Fence = nil
	require.ErrorIs(t, rt.Observe(context.Background(), invalid), ErrHealthIdentity)
	invalid = o
	invalid.AttemptID = ""
	require.ErrorIs(t, rt.Observe(context.Background(), invalid), ErrHealthIdentity)
	invalid = o
	copyFence := *o.Fence
	copyFence.HealthRevision++
	invalid.Fence = &copyFence
	require.ErrorIs(t, rt.Observe(context.Background(), invalid), ErrHealthIdentity)
	require.NoError(t, rt.Observe(context.Background(), o))
	require.Equal(t, 1, healthForTest(t, rt, "m", profile()).GoodStreak)
}

func TestHealthTerminalDeduplicationIsAtomic(t *testing.T) {
	rt, _ := testRuntime(t)
	checkHealthDuplicate(t, rt, "m")
}

func checkHealthDuplicate(t *testing.T, rt *Runtime, model string) {
	t.Helper()
	p := profile()
	o := terminalForHealth(t, rt, model, p, "same-attempt")
	var wg sync.WaitGroup
	errs := make(chan error, 16)
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); errs <- rt.Observe(context.Background(), o) }()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		require.NoError(t, err)
	}
	s := healthForTest(t, rt, model, p)
	require.Equal(t, 1, s.GoodStreak)
	require.Len(t, s.Samples, 1)
	for i := 0; i < 2; i++ {
		next := terminalForHealth(t, rt, model, p, opaqueID())
		require.NoError(t, rt.Observe(context.Background(), next))
	}
	s = healthForTest(t, rt, model, p)
	require.Equal(t, HealthRecovering, s.State)
	require.Zero(t, s.GoodStreak)
	require.NoError(t, rt.Observe(context.Background(), o))
	require.Equal(t, s, healthForTest(t, rt, model, p), "duplicate old-stage terminal must not qualify a new stage")
}

func TestHealthRecordExpiryDoesNotAcceptOldInFlightSuccess(t *testing.T) {
	rt, m := testRuntime(t)
	p := profile()
	o := terminalForHealth(t, rt, "m", p, "old-attempt")
	key := HealthRedisKey(1, "m", p, "default", "unknown", "http")
	require.Equal(t, healthRecordTTL, m.TTL(key))
	m.FastForward(2 * time.Minute)
	_, err := rt.FreezeHealth(context.Background(), 1, "m", p, "default", "unknown", "http")
	require.NoError(t, err)
	require.Equal(t, healthRecordTTL-2*time.Minute, m.TTL(key), "selection cannot extend observation lifetime")
	m.FastForward(healthRecordTTL)
	require.NoError(t, rt.Observe(context.Background(), o))
	require.False(t, m.Exists(key), "late terminal cannot recreate lost health state")
	fresh := terminalForHealth(t, rt, "m", p, "new-attempt")
	require.NotEqual(t, o.Fence.Generation, fresh.Fence.Generation)
	require.NoError(t, rt.Observe(context.Background(), o))
	require.Zero(t, healthForTest(t, rt, "m", p).GoodStreak)
	require.NoError(t, rt.Observe(context.Background(), fresh))
	require.Equal(t, 1, healthForTest(t, rt, "m", p).GoodStreak)
	m.FastForward(time.Minute)
	ttl := m.TTL(key)
	require.NoError(t, rt.Observe(context.Background(), fresh))
	require.Equal(t, ttl, m.TTL(key), "duplicate terminal cannot extend health TTL")
}

func TestHealthSemanticRevisionDoesNotResurrectOldProfileEvidence(t *testing.T) {
	rt, _ := testRuntime(t)
	p := profile()
	p.HealthRevision = 1
	old := terminalForHealth(t, rt, "m", p, "old-config")
	require.NoError(t, rt.Observe(context.Background(), old))
	changed := p
	changed.HealthRevision = 2
	changed.HealthThresholdMS++
	second := terminalForHealth(t, rt, "m", changed, "changed-config")
	require.NoError(t, rt.Observe(context.Background(), second))
	restored := p
	restored.HealthRevision = 3
	fresh := terminalForHealth(t, rt, "m", restored, "restored-config")
	require.NoError(t, rt.Observe(context.Background(), old))
	require.Zero(t, healthForTest(t, rt, "m", restored).GoodStreak)
	require.NoError(t, rt.Observe(context.Background(), fresh))
	require.Equal(t, 1, healthForTest(t, rt, "m", restored).GoodStreak)
	budgetOnly := restored
	budgetOnly.TotalBudgetMS++
	budgetOnly.MinAttemptWindowMS++
	require.Equal(t, HealthRedisKey(1, "m", restored, "default", "unknown", "http"), HealthRedisKey(1, "m", budgetOnly, "default", "unknown", "http"))
}

func TestHealthFreezePersistsCooldownStageBeforeDispatch(t *testing.T) {
	rt, _ := testRuntime(t)
	p := profile()
	o := terminalForHealth(t, rt, "m", p, "first")
	key := HealthRedisKey(1, "m", p, "default", "unknown", "http")
	s := HealthSnapshot{Generation: o.Fence.Generation, State: HealthOpen, StageRevision: 4, CooldownUntilMS: time.Now().Add(-time.Second).UnixMilli(), RecoveryRequirement: RecoveryLatency}
	raw, err := json.Marshal(s)
	require.NoError(t, err)
	require.NoError(t, rt.store.client.HSet(context.Background(), key, "snapshot", raw).Err())
	fence, err := rt.FreezeHealth(context.Background(), 1, "m", p, "default", "unknown", "http")
	require.NoError(t, err)
	require.Equal(t, uint64(5), fence.StageRevision)
	s = healthForTest(t, rt, "m", p)
	require.Equal(t, HealthHalfOpen, s.State)
	require.Equal(t, RecoveryLatency, s.RecoveryRequirement)
	o.AttemptID = "admitted-half-open"
	o.Fence = &fence
	o.HasSemanticOutput = true
	o.TTFT = time.Second
	require.NoError(t, rt.Observe(context.Background(), o))
	require.Equal(t, 1, healthForTest(t, rt, "m", p).GoodStreak)
}

func TestHealthReceiptRetentionRejectsAncientReplay(t *testing.T) {
	rt, _ := testRuntime(t)
	o := terminalForHealth(t, rt, "m", profile(), "ancient")
	o.At = time.Now().Add(-healthRecordTTL - time.Second)
	require.NoError(t, rt.Observe(context.Background(), o))
	require.Empty(t, healthForTest(t, rt, "m", profile()).Samples)
}

// This integration touches only keys containing its unique model identity. It
// does not flush Redis, start/stop resources, or use the service test database.
func TestHealthRealRedisTerminalDedupAndIncarnation(t *testing.T) {
	addr := os.Getenv("SCHEDULING_TEST_HEALTH_REDIS_ADDR")
	if addr == "" {
		t.Skip("SCHEDULING_TEST_HEALTH_REDIS_ADDR not set")
	}
	client := redis.NewClient(&redis.Options{Addr: addr, MaxRetries: 0})
	t.Cleanup(func() { _ = client.Close() })
	rt := NewRuntime(NewRedisStore(client))
	model := "phase-b-health-" + opaqueID()
	p := profile()
	key := HealthRedisKey(1, model, p, "default", "unknown", "http")
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		iter := client.Scan(ctx, 0, key+"*", 100).Iterator()
		for iter.Next(ctx) {
			_ = client.Del(ctx, iter.Val()).Err()
		}
	})
	checkHealthDuplicate(t, rt, model)
	checkHealthRotation(t, rt, model)
	before := terminalForHealth(t, rt, model, p, "before-reset")
	require.NoError(t, client.Del(context.Background(), key).Err())
	after := terminalForHealth(t, rt, model, p, "after-reset")
	require.NotEqual(t, before.Fence.Generation, after.Fence.Generation)
	require.NoError(t, rt.Observe(context.Background(), before))
	require.Zero(t, healthForTest(t, rt, model, p).GoodStreak)
	require.NoError(t, rt.Observe(context.Background(), after))
	require.Equal(t, 1, healthForTest(t, rt, model, p).GoodStreak)
}

func TestHealthReceiptEvictionCannotBeSeparatedFromSnapshot(t *testing.T) {
	rt, m := testRuntime(t)
	ctx := context.Background()
	o := terminalForHealth(t, rt, "m", profile(), "terminal")
	require.NoError(t, rt.Observe(ctx, o))
	key := HealthRedisKey(1, "m", profile(), "default", "unknown", "http")
	require.Equal(t, []string{key}, m.Keys(), "state and receipt must occupy a single evictable key")
	require.NoError(t, rt.Observe(ctx, o))
	require.Equal(t, 1, healthForTest(t, rt, "m", profile()).GoodStreak)
	require.NoError(t, rt.store.client.Del(ctx, key).Err())
	fresh := terminalForHealth(t, rt, "m", profile(), "fresh")
	require.NotEqual(t, o.Fence.Generation, fresh.Fence.Generation)
	require.NoError(t, rt.Observe(ctx, o))
	require.Zero(t, healthForTest(t, rt, "m", profile()).GoodStreak)
}

func TestHealthReceiptRotationPreservesHealthAndRejectsOldInflight(t *testing.T) {
	rt, _ := testRuntime(t)
	checkHealthRotation(t, rt, "rotation")
}

func checkHealthRotation(t *testing.T, rt *Runtime, model string) {
	t.Helper()
	ctx := context.Background()
	p := profile()
	o := terminalForHealth(t, rt, model, p, "saturation-terminal")
	key := HealthRedisKey(1, model, p, "default", "unknown", "http")
	state := HealthSnapshot{State: HealthRecovering, Generation: o.Fence.Generation, StageRevision: o.Fence.StageRevision, RecoveryStage: 1, GoodStreak: 1, RecoveryRequirement: RecoveryLatency, ChangedAtMS: time.Now().UnixMilli()}
	raw, err := json.Marshal(state)
	require.NoError(t, err)
	require.NoError(t, rt.store.client.HSet(ctx, key, "snapshot", raw).Err())
	receipts := make(map[string]any, healthReceiptLimit)
	for i := int64(0); i < healthReceiptLimit; i++ {
		receipts[fmt.Sprintf("observed:old-%d", i)] = o.At.UnixMilli()
	}
	require.NoError(t, rt.store.client.HSet(ctx, key, receipts).Err())
	o.HasSemanticOutput = true
	o.TTFT = time.Second
	var wg sync.WaitGroup
	errs := make(chan error, 16)
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); errs <- rt.Observe(ctx, o) }()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		require.NoError(t, err)
	}
	after := healthForTest(t, rt, model, p)
	require.NotEqual(t, state.Generation, after.Generation)
	require.Equal(t, HealthRecovering, after.State)
	require.Equal(t, 1, after.RecoveryStage)
	require.Equal(t, 2, after.GoodStreak)
	require.Equal(t, RecoveryLatency, after.RecoveryRequirement)
	require.Equal(t, state.StageRevision, after.StageRevision)
	require.Len(t, after.Samples, 1)
	count, err := rt.store.client.HLen(ctx, key).Result()
	require.NoError(t, err)
	require.Equal(t, int64(2), count)
	old := o
	old.AttemptID = "old-inflight-failure"
	old.Completed = false
	old.AttributableFailure = true
	require.NoError(t, rt.Observe(ctx, old))
	require.Equal(t, after, healthForTest(t, rt, model, p))
	fresh := terminalForHealth(t, rt, model, p, "fresh-failure")
	fresh.Completed = false
	fresh.AttributableFailure = true
	require.NoError(t, rt.Observe(ctx, fresh))
	require.Equal(t, HealthOpen, healthForTest(t, rt, model, p).State)
}
