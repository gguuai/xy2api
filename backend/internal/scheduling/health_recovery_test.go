package scheduling

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestAvailabilityBootstrapAndRecoveryWithoutLatencyWindow(t *testing.T) {
	for _, tc := range []struct {
		name     string
		semantic bool
		interval time.Duration
	}{
		{"nonstream", false, 45 * time.Second},
		{"long_stream", true, 45 * time.Second},
		{"sparse_stream", true, 181 * time.Second},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := HealthSnapshot{Generation: "dispatch-generation"}
			now := time.Now()
			for i := 0; i < 9; i++ {
				fence := HealthFence{Generation: s.Generation, StageRevision: s.StageRevision}
				s = AdvanceHealth(s, Observation{Fence: &fence, Profile: profile(), At: now.Add(time.Duration(i) * tc.interval), Completed: true, HasSemanticOutput: tc.semantic, TTFT: time.Second})
				switch i {
				case 1:
					require.Equal(t, HealthUnknown, s.State)
				case 2:
					require.Equal(t, HealthRecovering, s.State)
					require.Equal(t, 0, s.RecoveryStage)
					require.Equal(t, .1, RecoveryFactor(s))
				case 5:
					require.Equal(t, HealthRecovering, s.State)
					require.Equal(t, 1, s.RecoveryStage)
					require.Equal(t, .3, RecoveryFactor(s))
				case 8:
					require.Equal(t, HealthHealthy, s.State)
					require.Equal(t, 1., RecoveryFactor(s))
				}
			}
			if !tc.semantic {
				for _, sample := range s.Samples {
					require.False(t, sample.HasSemanticOutput, "nonstream completion must not manufacture TTFT")
				}
			}
		})
	}
}

func TestRecoveryNeedsNewTerminalAfterStageTimeAndNeverSkipsStage(t *testing.T) {
	p := profile()
	now := time.Now()
	s := HealthSnapshot{State: HealthRecovering, ChangedAtMS: now.UnixMilli()}
	for i := 1; i <= 100; i++ {
		s = AdvanceHealth(s, Observation{Profile: p, At: now.Add(time.Duration(i) * time.Millisecond), Completed: true})
	}
	require.Equal(t, 3, s.GoodStreak, "qualification counter must saturate")
	require.Zero(t, s.RecoveryStage)
	require.Zero(t, FreshHealth(s, now.Add(time.Hour), p).RecoveryStage, "clock alone must not promote")
	s = AdvanceHealth(s, Observation{Profile: p, At: now.Add(time.Hour), Completed: true})
	require.Equal(t, HealthRecovering, s.State)
	require.Equal(t, 1, s.RecoveryStage)
	require.Zero(t, s.GoodStreak)
}

func TestLatencyRecoveryRequirementSurvivesNonstreamAndOpen(t *testing.T) {
	p := profile()
	now := time.Now()
	s := HealthSnapshot{State: HealthDegraded, RecoveryRequirement: RecoveryLatency, CooldownUntilMS: now.UnixMilli()}
	emit := func(semantic bool, ms int64, fail bool) {
		now = now.Add(time.Second)
		s = AdvanceHealth(s, Observation{Profile: p, At: now, Completed: !fail, HasSemanticOutput: semantic, TTFT: time.Duration(ms) * time.Millisecond, AttributableFailure: fail})
	}
	for i := 0; i < 5; i++ {
		emit(false, 0, false)
	}
	require.Equal(t, HealthHalfOpen, s.State)
	require.Zero(t, s.GoodStreak)
	emit(true, 1000, false)
	emit(true, 1000, false)
	emit(false, 0, false)
	require.Equal(t, 2, s.GoodStreak, "missing latency must not erase fast evidence")
	emit(true, 7000, false)
	require.Zero(t, s.GoodStreak, "real terminal slower than R breaks fast qualification")
	for i := 0; i < 3; i++ {
		emit(true, 1000, false)
	}
	require.Equal(t, HealthRecovering, s.State)
	emit(false, 0, true)
	require.Equal(t, HealthOpen, s.State)
	require.Equal(t, RecoveryLatency, s.RecoveryRequirement)
	now = time.UnixMilli(s.CooldownUntilMS)
	for i := 0; i < 5; i++ {
		emit(false, 0, false)
	}
	require.Equal(t, HealthHalfOpen, s.State)
	require.Zero(t, s.GoodStreak)
	for i := 0; i < 3; i++ {
		emit(true, 1000, false)
	}
	require.Equal(t, HealthRecovering, s.State)
	for stage := 0; stage < 2; stage++ {
		now = time.UnixMilli(s.ChangedAtMS).Add(31 * time.Second)
		for i := 0; i < 3; i++ {
			emit(false, 0, false)
		}
		require.Equal(t, stage, s.RecoveryStage)
		for i := 0; i < 3; i++ {
			emit(true, 1000, false)
		}
	}
	require.Equal(t, HealthHealthy, s.State)
}

func TestHealthFenceRejectsOldIncarnationAndOldStagePromotions(t *testing.T) {
	p := profile()
	p.HealthRevision = 7
	now := time.Now()
	s := HealthSnapshot{State: HealthRecovering, Generation: "new", StageRevision: 4, ChangedAtMS: now.Add(-time.Minute).UnixMilli(), GoodStreak: 2}
	o := Observation{Profile: p, At: now, Completed: true, HasSemanticOutput: true, TTFT: time.Second, Fence: &HealthFence{Generation: "old", StageRevision: 4, HealthRevision: 7}}
	require.Equal(t, s, AdvanceHealth(s, o))
	o.Fence.Generation = "new"
	o.Fence.HealthRevision = 6
	require.Equal(t, s, AdvanceHealth(s, o))
	o.Fence.HealthRevision = 7
	o.Fence.StageRevision = 3
	next := AdvanceHealth(s, o)
	require.Equal(t, 2, next.GoodStreak)
	require.Zero(t, next.RecoveryStage)
	o.Completed = false
	o.AttributableFailure = true
	next = AdvanceHealth(s, o)
	require.Equal(t, HealthOpen, next.State, "late real failures still protect the same incarnation")
	require.Greater(t, next.StageRevision, s.StageRevision)
}

func TestOldInFlightSuccessAfterCooldownCannotQualifyHalfOpen(t *testing.T) {
	p := profile()
	now := time.Now()
	s := HealthSnapshot{State: HealthOpen, Generation: "same", StageRevision: 4, CooldownUntilMS: now.Add(-time.Second).UnixMilli()}
	o := Observation{Profile: p, At: now, Completed: true, Fence: &HealthFence{Generation: "same", StageRevision: 4}}
	next := AdvanceHealth(s, o)
	require.Equal(t, HealthHalfOpen, next.State)
	require.Equal(t, uint64(5), next.StageRevision)
	require.Zero(t, next.GoodStreak)
}

func TestAvailabilityRemainsProvenWhenLatencySamplesAge(t *testing.T) {
	p := profile()
	now := time.Now()
	s := HealthSnapshot{State: HealthHealthy, UpdatedAtMS: now.UnixMilli()}
	for i := 0; i < 5; i++ {
		s.Samples = append(s.Samples, HealthSample{AtMS: now.UnixMilli(), HasSemanticOutput: true, Completed: true, TTFTMS: 1000})
	}
	s = FreshHealth(s, now.Add(121*time.Second), p)
	require.Equal(t, HealthHealthy, s.State)
	_, comparable := ConservativeWaitScore(s, p, now.Add(121*time.Second))
	require.False(t, comparable)
	require.False(t, probeHealth(s.State), "expired latency alone must not restore concurrency one")
}

func TestNegativeEvidencePrecedesBootstrapPromotion(t *testing.T) {
	p := profile()
	now := time.Now()
	s := HealthSnapshot{}
	for i := 0; i < 3; i++ {
		s = AdvanceHealth(s, Observation{Profile: p, At: now.Add(time.Duration(i) * time.Second), Completed: true, HasSemanticOutput: true, TTFT: 9 * time.Second})
	}
	require.Equal(t, HealthDegraded, s.State)
	require.Equal(t, RecoveryLatency, s.RecoveryRequirement)
	require.Zero(t, s.GoodStreak)
}
