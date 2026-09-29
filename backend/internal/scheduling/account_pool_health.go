package scheduling

import "time"

const accountPoolCooldown = 30 * time.Second

// freshAccountPoolHealth intentionally has no UNKNOWN startup throttle, slow
// score, or percentage recovery ladder. A cooled account admits one probe.
func freshAccountPoolHealth(s HealthSnapshot, now time.Time) HealthSnapshot {
	switch s.State {
	case HealthOpen, HealthDegraded:
		if now.UnixMilli() < s.CooldownUntilMS {
			s.State = HealthOpen
			return s
		}
		s.State = HealthHalfOpen
		s.StageRevision++
		s.ChangedAtMS = now.UnixMilli()
	case HealthHalfOpen, HealthRecovering:
		s.State = HealthHalfOpen
	default:
		s.State = HealthHealthy
	}
	return s
}

func advanceAccountPoolHealth(old HealthSnapshot, o Observation) HealthSnapshot {
	if o.Excluded || (!o.Completed && !o.AttributableFailure && !o.FirstOutputTimeout) {
		return old
	}
	if o.Fence != nil && (o.Fence.Generation != old.Generation || o.Fence.HealthRevision != o.Profile.HealthRevision) {
		return old
	}
	s := freshAccountPoolHealth(old, o.At)
	now := o.At.UnixMilli()
	if now > s.UpdatedAtMS {
		s.UpdatedAtMS = now
	}
	// Bounded terminal evidence is diagnostic only. Neither latency samples
	// nor the success counter participate in priority, weights, or recovery.
	samples := make([]HealthSample, 0, 20)
	for _, sample := range s.Samples {
		if sample.AtMS >= now-sampleWindowMS {
			samples = append(samples, sample)
		}
	}
	samples = append(samples, HealthSample{AtMS: now, TTFTMS: o.TTFT.Milliseconds(), Timeout: o.FirstOutputTimeout, AttributableFailure: o.AttributableFailure, HasSemanticOutput: o.HasSemanticOutput, Completed: o.Completed})
	if len(samples) > 20 {
		samples = samples[len(samples)-20:]
	}
	s.Samples = samples
	if o.AttributableFailure || o.FirstOutputTimeout {
		until := o.At.Add(accountPoolCooldown).UnixMilli()
		if o.RetryAfter.UnixMilli() > until {
			until = o.RetryAfter.UnixMilli()
		}
		if s.CooldownUntilMS > until {
			until = s.CooldownUntilMS
		}
		s.State, s.CooldownUntilMS = HealthOpen, until
		s.ChangedAtMS = now
		s.StageRevision++
		s.GoodStreak, s.RecoveryStage = 0, 0
		return s
	}
	// A terminal from an earlier admission cannot clear a later cooldown or
	// claim success in the stage that did not admit it.
	sameStage := o.Fence == nil || o.Fence.StageRevision == s.StageRevision
	if sameStage && s.State != HealthOpen && s.GoodStreak < 20 {
		s.GoodStreak++
	}
	if s.State == HealthHalfOpen && sameStage {
		s.State = HealthHealthy
		s.CooldownUntilMS, s.ChangedAtMS = 0, now
		s.StageRevision++
		s.RecoveryFailures, s.RecoveryStage = 0, 0
	}
	return s
}

// ClassifyAccountPoolFailure reuses verified failure scopes and replay guards.
// Ordinary attributable transport/5xx/timeouts also create an authoritative PG
// cooldown. PG's existing probe ticket then fences unresolved recovery attempts
// even if the Redis probe lease expires or shared cache state is lost.
func ClassifyAccountPoolFailure(e FailureEvidence, a FailureAdmission, now time.Time) FailureDecision {
	d := ClassifyFailure(e, a, now)
	if e.AttemptTimeout && d.Class == "unclassified" {
		d.Class, d.Reason, d.Effect, d.Scope, d.Key = "attempt_timeout", "outcome_unknown", "observe_failure", "account_model", a.ModelKey()
	}
	if d.Effect == "observe_failure" && d.Key != "" {
		d.Effect = "cooldown"
		d.Until = now.Add(accountPoolCooldown)
		if e.RetryAfter.After(d.Until) {
			d.Until = e.RetryAfter
		}
	}
	return d
}
