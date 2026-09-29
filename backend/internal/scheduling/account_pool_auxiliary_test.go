package scheduling

import (
	"context"
	"encoding/json"
	"github.com/stretchr/testify/require"
	"testing"
	"time"
)

func TestSchedulingModeAuxiliaryWeightStreamDoesNotChangeGeneration(t *testing.T) {
	r, _ := testRuntime(t)
	ctx := context.Background()
	req := accountPoolRequest()
	before, err := r.InspectSelection(ctx, req)
	require.NoError(t, err)
	counts := map[int64]int{}
	for i := 0; i < 20; i++ {
		d, e := r.SelectAuxiliary(ctx, req)
		require.NoError(t, e)
		counts[d.AccountID]++
		require.Empty(t, d.ReservationID)
		require.Empty(t, d.ProbeToken)
		require.False(t, d.Probe)
	}
	require.Equal(t, map[int64]int{1: 14, 2: 6}, counts)
	after, err := r.InspectSelection(ctx, req)
	require.NoError(t, err)
	require.Equal(t, before.Selected.AccountID, after.Selected.AccountID)
	req.Policy.Accounts[0].Weight = 0
	d, err := r.SelectAuxiliary(ctx, req)
	require.NoError(t, err)
	require.EqualValues(t, 2, d.AccountID)
}
func TestSchedulingModeAuxiliaryCannotConsumeRecoveryProbe(t *testing.T) {
	r, m := testRuntime(t)
	ctx := context.Background()
	req := accountPoolRequest()
	for _, id := range []int64{1, 2} {
		raw, err := json.Marshal(HealthSnapshot{State: HealthOpen, CooldownUntilMS: req.Now.Add(-time.Second).UnixMilli()})
		require.NoError(t, err)
		require.NoError(t, r.store.client.HSet(ctx, HealthRedisKey(id, req.Policy.Model, req.Profile, "", "", ""), "snapshot", string(raw)).Err())
	}
	d, err := r.SelectAuxiliary(ctx, req)
	require.NoError(t, err)
	require.EqualValues(t, 3, d.AccountID)
	require.Empty(t, d.ProbeToken)
	for _, key := range m.Keys() {
		require.NotContains(t, key, "scheduling:probe:")
	}
}
