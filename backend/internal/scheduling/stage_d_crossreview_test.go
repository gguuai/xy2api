package scheduling

import (
	"context"
	"encoding/json"
	"github.com/stretchr/testify/require"
	"testing"
	"time"
)

// Independent cross-review: final health admission must preserve the explicit
// degraded best-effort exception without accepting an OPEN state.
func TestStageDReviewDegradedAdmissionAndMissingGeneration(t *testing.T) {
	rt, req := healthSelectionFixture(t)
	req.Policy.AllDegraded = AllDegradedBestEffort
	ctx := context.Background()
	ds, err := rt.Preview(ctx, req)
	require.NoError(t, err)
	fence, err := freezeSelectedForTest(rt, req, ds[0])
	require.NoError(t, err)
	require.NotEmpty(t, fence.Generation)
	ds, err = rt.Preview(ctx, req)
	require.NoError(t, err)
	_, err = freezeSelectedForTest(rt, req, ds[0])
	require.NoError(t, err, "a refreshed initial selection must not livelock")
	key := HealthRedisKey(1, req.Policy.Model, req.Profile, req.Reasoning, req.ContextBucket, req.Transport, req.Candidates[0].HealthIdentity)
	degraded := HealthSnapshot{State: HealthDegraded, Generation: fence.Generation, StageRevision: 1, RecoveryRequirement: RecoveryLatency, CooldownUntilMS: time.Now().Add(time.Minute).UnixMilli()}
	raw, err := json.Marshal(degraded)
	require.NoError(t, err)
	require.NoError(t, rt.store.client.HSet(ctx, key, "snapshot", raw).Err())
	ds, err = rt.Preview(ctx, req)
	require.NoError(t, err)
	require.Len(t, ds, 1)
	require.Equal(t, HealthDegraded, ds[0].HealthState)
	admitted, err := freezeSelectedForTest(rt, req, ds[0])
	require.NoError(t, err)
	require.Equal(t, HealthDegraded, admitted.State)
	degraded.State = HealthOpen
	degraded.StageRevision++
	raw, err = json.Marshal(degraded)
	require.NoError(t, err)
	require.NoError(t, rt.store.client.HSet(ctx, key, "snapshot", raw).Err())
	_, err = freezeSelectedForTest(rt, req, ds[0])
	require.ErrorIs(t, err, ErrHealthSelectionStale)
}
func TestStageDReviewMissingTTFTIsNotFastAndFailureRemainsTerminal(t *testing.T) {
	p := profile()
	now := time.Now()
	state := HealthSnapshot{State: HealthHalfOpen, Generation: "same", RecoveryRequirement: RecoveryLatency, GoodStreak: 2}
	frozen := HealthFence{Generation: "same"}
	partial := Observation{Profile: p, Fence: &frozen, At: now, HasSemanticOutput: true, TTFT: time.Millisecond}
	require.Equal(t, state, AdvanceHealth(state, partial), "first text is not a whole-request success")
	missing := partial
	missing.Completed = true
	missing.HasSemanticOutput = false
	next := AdvanceHealth(state, missing)
	require.Equal(t, 2, next.GoodStreak)
	require.Equal(t, HealthHalfOpen, next.State)
	_, known := ConservativeWaitScore(next, p, now)
	require.False(t, known)
	failed := partial
	failed.AttributableFailure = true
	next = AdvanceHealth(state, failed)
	require.Equal(t, HealthOpen, next.State)
	require.Equal(t, RecoveryLatency, next.RecoveryRequirement)
}
