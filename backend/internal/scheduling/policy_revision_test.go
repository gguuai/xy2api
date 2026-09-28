package scheduling

import (
	"context"
	"github.com/stretchr/testify/require"
	"sync"
	"testing"
)

func TestProfileOverlapSelectors(t *testing.T) {
	base := Policy{Model: "m"}
	cases := []struct {
		name     string
		profiles []LatencyProfile
		invalid  bool
	}{
		{"wildcard tie", []LatencyProfile{{Name: "a"}, {Name: "b"}}, true},
		{"canonical selector tie", []LatencyProfile{{Name: "a", Reasoning: " HIGH ", Transport: "anthropic"}, {Name: "b", Reasoning: "high", Transport: "messages"}}, true},
		{"adjacent half open", []LatencyProfile{{Name: "a", ContextMaxTokens: 8192}, {Name: "b", ContextMinTokens: 8192}}, false},
		{"intersecting", []LatencyProfile{{Name: "a", ContextMaxTokens: 8193}, {Name: "b", ContextMinTokens: 8192}}, true},
		{"more specific", []LatencyProfile{{Name: "a"}, {Name: "b", Reasoning: "high"}}, false},
		{"negative bound", []LatencyProfile{{Name: "a", ContextMaxTokens: -1}}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			base.Profiles = tc.profiles
			err := ValidatePolicy(base)
			if tc.invalid {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
		})
	}
	legacy := Policy{Profiles: []LatencyProfile{{Name: "first"}, {Name: "second"}}}
	got, ok := ResolveProfileForTransport(legacy, "", -1, "responses")
	require.True(t, ok)
	require.Equal(t, "first", got.Name)
	require.Len(t, ProfileAmbiguities(legacy), 1)
	p := Policy{Profiles: []LatencyProfile{{Name: "bounded", ContextMaxTokens: 8192}, {Name: "unknown-safe"}}}
	got, ok = ResolveProfileForTransport(p, "", -1, "responses")
	require.True(t, ok)
	require.Equal(t, "unknown-safe", got.Name)
}
func TestPostgresPolicyHealthRevisionAndInheritance(t *testing.T) {
	s, db := isolatedControlStore(t)
	ctx := context.Background()
	p := Policy{Model: "revisions", Enabled: true, Profiles: []LatencyProfile{{Name: "base", HealthThresholdMS: 8000, RecoveryThresholdMS: 6000, AttemptTimeoutMS: 12000, TotalBudgetMS: 25000, MinAttemptWindowMS: 8000}}}
	first, err := s.PutPolicy(ctx, p, 0)
	require.NoError(t, err)
	rev := first.Policy.Profiles[0].HealthRevision
	require.Positive(t, rev)
	p = *first.Policy
	p.Profiles[0].HealthRevision = 987654
	p.Profiles[0].TotalBudgetMS = 30000
	p.Profiles[0].MinAttemptWindowMS = 7000
	unchanged, err := s.PutPolicy(ctx, p, 1)
	require.NoError(t, err)
	require.Equal(t, rev, unchanged.Policy.Profiles[0].HealthRevision)
	p = *unchanged.Policy
	p.Profiles[0].HealthThresholdMS = 9000
	changed, err := s.PutPolicy(ctx, p, 2)
	require.NoError(t, err)
	require.Greater(t, changed.Policy.Profiles[0].HealthRevision, rev)
	p = *changed.Policy
	p.Profiles[0].HealthThresholdMS = 8000
	reverted, err := s.PutPolicy(ctx, p, 3)
	require.NoError(t, err)
	require.Greater(t, reverted.Policy.Profiles[0].HealthRevision, changed.Policy.Profiles[0].HealthRevision)
	reset, err := s.PutPolicyWithHealthReset(ctx, *reverted.Policy, 4, []string{"base"})
	require.NoError(t, err)
	require.Greater(t, reset.Policy.Profiles[0].HealthRevision, reverted.Policy.Profiles[0].HealthRevision)
	_, err = s.PutPolicyWithHealthReset(ctx, *reset.Policy, 5, []string{"missing"})
	require.ErrorIs(t, err, ErrInvalidControl)
	group := *reset.Policy
	group.GroupID = 2
	override, err := s.PutPolicy(ctx, group, 0)
	require.NoError(t, err)
	require.NotEqual(t, reset.Policy.Profiles[0].HealthRevision, override.Policy.Profiles[0].HealthRevision)
	restored, err := s.RestorePolicyInheritance(ctx, 2, p.Model, override.Version)
	require.NoError(t, err)
	require.Nil(t, restored.Policy)
	require.EqualValues(t, 2, restored.Version)
	read, err := s.GetPolicy(ctx, 2, p.Model)
	require.NoError(t, err)
	require.Nil(t, read.Policy)
	require.EqualValues(t, 2, read.Version)
	_, err = s.PutPolicy(ctx, group, 0)
	require.ErrorIs(t, err, ErrVersionConflict)
	_, err = s.PutPolicy(ctx, group, 1)
	require.ErrorIs(t, err, ErrVersionConflict)
	renewed, err := s.PutPolicy(ctx, group, 2)
	require.NoError(t, err)
	require.Greater(t, renewed.Policy.Profiles[0].HealthRevision, override.Policy.Profiles[0].HealthRevision)
	_, err = s.RestorePolicyInheritance(ctx, 0, p.Model, 5)
	require.ErrorIs(t, err, ErrInvalidControl)
	// Historical ambiguous JSON remains inspectable and deterministically diagnosed.
	_, err = db.Exec("INSERT INTO scheduling_policies(group_id,model,version,policy) VALUES(0,'historical',1,'{\"model\":\"historical\",\"profiles\":[{\"name\":\"a\"},{\"name\":\"b\"}]}'::jsonb)")
	require.NoError(t, err)
	historical, err := s.GetPolicy(ctx, 0, "historical")
	require.NoError(t, err)
	require.Len(t, historical.Diagnostics, 1)
}
func TestPostgresPolicyConcurrentCreationCAS(t *testing.T) {
	s, _ := isolatedControlStore(t)
	ctx := context.Background()
	p := Policy{Model: "race", Profiles: []LatencyProfile{{Name: "base"}}}
	var wg sync.WaitGroup
	results := make(chan error, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); _, err := s.PutPolicy(ctx, p, 0); results <- err }()
	}
	wg.Wait()
	close(results)
	successes, conflicts := 0, 0
	for err := range results {
		if err == nil {
			successes++
		} else {
			require.ErrorIs(t, err, ErrVersionConflict)
			conflicts++
		}
	}
	require.Equal(t, 1, successes)
	require.Equal(t, 1, conflicts)
}
