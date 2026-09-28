package scheduling

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestStableHealthIdentityPreservesTokenRefreshAndSeparatesPrincipal(t *testing.T) {
	credentials := map[string]any{"chatgpt_account_id": "account-a", "chatgpt_user_id": "user-a", "access_token": "secret-old", "refresh_token": "refresh-old", "expires_at": "before", "base_url": "https://provider.example/v1/"}
	before := StableHealthIdentity("openai", "oauth", credentials, nil)
	credentials["access_token"] = "secret-new"
	credentials["refresh_token"] = "refresh-new"
	credentials["expires_at"] = "after"
	credentials["model_mapping"] = map[string]any{"a": "b"}
	require.Equal(t, before, StableHealthIdentity("openai", "oauth", credentials, nil))
	credentials["chatgpt_account_id"] = "account-b"
	require.NotEqual(t, before, StableHealthIdentity("openai", "oauth", credentials, nil))
	credentials["chatgpt_account_id"] = "account-a"
	credentials["base_url"] = "https://different.example/v1"
	require.NotEqual(t, before, StableHealthIdentity("openai", "oauth", credentials, nil))
	require.NotEqual(t, before, StableHealthIdentity("anthropic", "oauth", credentials, nil))
	require.Len(t, before, 64)
	require.NotContains(t, before, "secret-old")
}

func TestStableHealthIdentityUsesAPIKeyFallbackOnlyWithoutPrincipal(t *testing.T) {
	credentials := map[string]any{"api_key": "key-a", "base_url": "https://provider.example/v1"}
	before := StableHealthIdentity("openai", "apikey", credentials, nil)
	credentials["api_key"] = "key-b"
	require.NotEqual(t, before, StableHealthIdentity("openai", "apikey", credentials, nil))
	credentials["organization_id"] = "explicit-org"
	proven := StableHealthIdentity("openai", "apikey", credentials, nil)
	credentials["api_key"] = "key-c"
	require.Equal(t, proven, StableHealthIdentity("openai", "apikey", credentials, nil))
	endpoints := map[string]any{"api_base_urls": map[string]any{"responses": "https://provider.example/responses/", "chat": "https://provider.example/chat"}, "organization_id": "explicit-org"}
	id := StableHealthIdentity("openai", "apikey", endpoints, nil)
	endpoints["api_base_urls"] = map[string]string{"chat": "https://provider.example/chat", "responses": "https://provider.example/responses"}
	require.Equal(t, id, StableHealthIdentity("openai", "apikey", endpoints, nil))
	require.NotEqual(t, id, StableHealthIdentity("openai", "apikey", endpoints, map[string]any{"custom_base_url_enabled": true, "custom_base_url": "https://custom.example"}))
}

func TestHealthScopeIsolatesStableIdentityAndEscapesProfileFields(t *testing.T) {
	p := profile()
	other := p
	other.Name = "n|" + p.Name
	require.NotEqual(t, HealthRedisKey(1, "m|n", p, "default", "unknown", "http"), HealthRedisKey(1, "m", other, "default", "unknown", "http"))
	rt, _ := testRuntime(t)
	ctx := context.Background()
	a := "stable-account-a"
	b := "stable-account-b"
	fence, err := rt.FreezeHealth(ctx, 1, "m", p, "default", "unknown", "http", a)
	require.NoError(t, err)
	obs := Observation{AttemptID: "attempt-a", HealthIdentity: a, Fence: &fence, AccountID: 1, Model: "m", Profile: p, Reasoning: "default", ContextBucket: "unknown", Transport: "http", At: time.Now(), Completed: true}
	require.NoError(t, rt.Observe(ctx, obs))
	_, err = rt.FreezeHealth(ctx, 1, "m", p, "default", "unknown", "http", b)
	require.NoError(t, err)
	req := SelectionRequest{Policy: Policy{Model: "m"}, Profile: p, Reasoning: "default", ContextBucket: "unknown", Transport: "http", Candidates: []Candidate{{AccountID: 1, HealthIdentity: b}}}
	states, err := rt.store.Snapshots(ctx, req)
	require.NoError(t, err)
	require.Zero(t, states[1].GoodStreak)
	require.NoError(t, rt.Observe(ctx, obs))
	states, err = rt.store.Snapshots(ctx, req)
	require.NoError(t, err)
	require.Zero(t, states[1].GoodStreak)
	obs.HealthIdentity = b
	require.ErrorIs(t, rt.Observe(ctx, obs), ErrHealthIdentity)
}

func healthSelectionFixture(t *testing.T) (*Runtime, SelectionRequest) {
	t.Helper()
	rt, _ := testRuntime(t)
	c := candidate(1, 0)
	c.HealthIdentity = "stable-principal"
	return rt, SelectionRequest{Policy: Policy{Model: "m"}, Profile: profile(), Reasoning: "default", ContextBucket: "unknown", Transport: "http", Candidates: []Candidate{c}}
}
func freezeSelectedForTest(rt *Runtime, req SelectionRequest, d Decision) (HealthFence, error) {
	return rt.FreezeSelectedHealth(context.Background(), d.AccountID, req.Policy.Model, req.Profile, req.Reasoning, req.ContextBucket, req.Transport, req.Candidates[0].HealthIdentity, d)
}

func TestHealthAdmissionInitializesOnceAndRejectsStaleGeneration(t *testing.T) {
	rt, req := healthSelectionFixture(t)
	ctx := context.Background()
	decisions, err := rt.Preview(ctx, req)
	require.NoError(t, err)
	require.Len(t, decisions, 1)
	first := decisions[0]
	require.NotNil(t, first.HealthFence)
	require.Empty(t, first.HealthFence.Generation)
	fence, err := freezeSelectedForTest(rt, req, first)
	require.NoError(t, err)
	require.NotEmpty(t, fence.Generation)
	require.Equal(t, HealthUnknown, fence.State)
	_, err = freezeSelectedForTest(rt, req, first)
	require.ErrorIs(t, err, ErrHealthSelectionStale)
	decisions, err = rt.Preview(ctx, req)
	require.NoError(t, err)
	_, err = freezeSelectedForTest(rt, req, decisions[0])
	require.NoError(t, err)
	key := HealthRedisKey(1, "m", req.Profile, "default", "unknown", "http", req.Candidates[0].HealthIdentity)
	require.NoError(t, rt.store.client.Del(ctx, key).Err())
	_, err = freezeSelectedForTest(rt, req, decisions[0])
	require.ErrorIs(t, err, ErrHealthSelectionStale)
}

func TestHealthAdmissionRejectsOpenAndChangedStageWithoutWriting(t *testing.T) {
	for _, state := range []HealthState{HealthOpen, HealthRecovering} {
		t.Run(string(state), func(t *testing.T) {
			rt, req := healthSelectionFixture(t)
			ctx := context.Background()
			_, err := rt.FreezeHealth(ctx, 1, "m", req.Profile, "default", "unknown", "http", req.Candidates[0].HealthIdentity)
			require.NoError(t, err)
			decisions, err := rt.Preview(ctx, req)
			require.NoError(t, err)
			selected := decisions[0]
			key := HealthRedisKey(1, "m", req.Profile, "default", "unknown", "http", req.Candidates[0].HealthIdentity)
			changed := HealthSnapshot{Generation: selected.HealthFence.Generation, StageRevision: selected.HealthFence.StageRevision + 1, State: state, CooldownUntilMS: time.Now().Add(time.Minute).UnixMilli()}
			raw, err := json.Marshal(changed)
			require.NoError(t, err)
			require.NoError(t, rt.store.client.HSet(ctx, key, "snapshot", raw).Err())
			_, err = freezeSelectedForTest(rt, req, selected)
			require.ErrorIs(t, err, ErrHealthSelectionStale)
			after, err := rt.store.client.HGet(ctx, key, "snapshot").Bytes()
			require.NoError(t, err)
			require.Equal(t, raw, after)
			if state == HealthOpen {
				_, err = freezeSelectedForTest(rt, req, Decision{AccountID: 1, Reason: "protocol_owner"})
				require.ErrorIs(t, err, ErrHealthSelectionStale)
			}
		})
	}
}

func TestHealthAdmissionMatchesCooldownTransitionAndIdentity(t *testing.T) {
	rt, req := healthSelectionFixture(t)
	ctx := context.Background()
	key := HealthRedisKey(1, "m", req.Profile, "default", "unknown", "http", req.Candidates[0].HealthIdentity)
	old := HealthSnapshot{Generation: "known", StageRevision: 4, State: HealthOpen, CooldownUntilMS: time.Now().Add(-time.Second).UnixMilli()}
	raw, err := json.Marshal(old)
	require.NoError(t, err)
	require.NoError(t, rt.store.client.HSet(ctx, key, "snapshot", raw).Err())
	decisions, err := rt.Preview(ctx, req)
	require.NoError(t, err)
	require.Len(t, decisions, 1)
	require.Equal(t, uint64(5), decisions[0].HealthFence.StageRevision)
	fence, err := freezeSelectedForTest(rt, req, decisions[0])
	require.NoError(t, err)
	require.Equal(t, HealthHalfOpen, fence.State)
	require.Equal(t, uint64(5), fence.StageRevision)
	req.Candidates[0].HealthIdentity = "changed-principal"
	_, err = freezeSelectedForTest(rt, req, decisions[0])
	require.ErrorIs(t, err, ErrHealthSelectionStale)
}

func TestHealthActualModelSnapshotAndFenceIsolation(t *testing.T) {
	rt, req := healthSelectionFixture(t)
	ctx := context.Background()
	p := req.Profile
	o := terminalForHealth(t, rt, "model-A", p, "attempt-A")
	require.NoError(t, rt.Observe(ctx, o))
	copyObservation := o
	copyObservation.Model = "model-B"
	require.ErrorIs(t, rt.Observe(ctx, copyObservation), ErrHealthIdentity, "an A terminal may not be redirected into B")
	require.Zero(t, healthForTest(t, rt, "model-B", p).GoodStreak)

	req.Candidates[0].HealthIdentity = ""
	req.Candidates[0].HealthModel = "model-A"
	states, err := rt.store.Snapshots(ctx, req)
	require.NoError(t, err)
	require.Equal(t, 1, states[1].GoodStreak)
	decisions, err := rt.Preview(ctx, req)
	require.NoError(t, err)
	require.Len(t, decisions, 1)
	require.Equal(t, "model-A", decisions[0].HealthFence.Model)
	_, err = rt.FreezeSelectedHealth(ctx, 1, "model-B", p, req.Reasoning, req.ContextBucket, req.Transport, "", decisions[0])
	require.ErrorIs(t, err, ErrHealthSelectionStale)
}
