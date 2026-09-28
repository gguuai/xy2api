package scheduling

import "time"

const sampleWindowMS int64 = 120000
const cooldownMS int64 = 30000

// FreshHealth may open a new probe stage after cooldown. The rolling latency
// window is not a lifetime for proven availability: missing TTFT never restores
// the cold-start concurrency cap. Redis record expiry remains a separate reset.
func FreshHealth(s HealthSnapshot, now time.Time, profile LatencyProfile) HealthSnapshot {
	if s.State == "" {
		s.State = HealthUnknown
	}
	if s.RecoveryRequirement == "" {
		s.RecoveryRequirement = RecoveryAvailability
		if s.State == HealthDegraded {
			s.RecoveryRequirement = RecoveryLatency
		}
	}
	// Unconfigured latency retains the existing observe-only startup behavior.
	if profile.ObserveOnly() && s.State == HealthUnknown {
		s.State = HealthHealthy
	}
	if (s.State == HealthOpen || s.State == HealthDegraded) && now.UnixMilli() >= s.CooldownUntilMS {
		s.State = HealthHalfOpen
		s.GoodStreak = 0
		s.ChangedAtMS = now.UnixMilli()
		s.StageRevision++
	}
	return s
}

// AdvanceHealth accepts terminal attempt observations. Progress chunks cannot
// claim whole-request success or promote an account before a later failed event.
// RedisStore supplies generation validation and atomic attempt-id deduplication.
func AdvanceHealth(old HealthSnapshot, o Observation) HealthSnapshot {
	if o.Excluded || (!o.Completed && !o.AttributableFailure && !o.FirstOutputTimeout) {
		return old
	}
	if o.Fence != nil && (o.Fence.Generation != old.Generation || o.Fence.HealthRevision != o.Profile.HealthRevision) {
		return old
	}
	s := FreshHealth(old, o.At, o.Profile)
	now := o.At.UnixMilli()
	if now > s.UpdatedAtMS {
		s.UpdatedAtMS = now
	}
	samples := make([]HealthSample, 0, 20)
	for _, v := range s.Samples {
		if v.AtMS >= now-sampleWindowMS {
			samples = append(samples, v)
		}
	}
	v := HealthSample{AtMS: now, TTFTMS: o.TTFT.Milliseconds(), Timeout: o.FirstOutputTimeout, AttributableFailure: o.AttributableFailure, HasSemanticOutput: o.HasSemanticOutput, Completed: o.Completed}
	samples = append(samples, v)
	if len(samples) > 20 {
		samples = samples[len(samples)-20:]
	}
	s.Samples = samples
	failure := o.AttributableFailure || o.FirstOutputTimeout
	slow := !o.Profile.ObserveOnly() && ((o.HasSemanticOutput && v.TTFTMS > o.Profile.HealthThresholdMS) || (o.FirstOutputTimeout && v.TTFTMS >= o.Profile.HealthThresholdMS))
	availabilitySuccess := o.Completed && !failure
	latencyFast := availabilitySuccess && o.HasSemanticOutput && v.TTFTMS <= o.Profile.RecoveryThresholdMS
	sameStage := o.Fence == nil || o.Fence.StageRevision == s.StageRevision
	cooldown := func(state HealthState, recoveryFailure bool) {
		if state == HealthDegraded || slow {
			s.RecoveryRequirement = RecoveryLatency
		}
		if recoveryFailure {
			s.RecoveryFailures++
		}
		delay := cooldownMS
		for i := 0; i < s.RecoveryFailures && delay < 300000; i++ {
			delay *= 2
		}
		if delay > 300000 {
			delay = 300000
		}
		until := now + delay
		if !o.RetryAfter.IsZero() && o.RetryAfter.UnixMilli() > until {
			until = o.RetryAfter.UnixMilli()
		}
		// A delayed failure cannot shorten a still-live provider cooldown.
		if s.CooldownUntilMS > until {
			until = s.CooldownUntilMS
		}
		s.State = state
		s.CooldownUntilMS = until
		s.ChangedAtMS = now
		s.GoodStreak = 0
		s.RecoveryStage = 0
		s.StageRevision++
	}
	// An explicit provider Retry-After is an immediate availability gate.
	if failure && o.RetryAfter.After(o.At) {
		cooldown(HealthOpen, s.State == HealthHalfOpen || s.State == HealthRecovering)
		return s
	}
	// A response already in flight before opening cannot close the breaker early.
	if s.State == HealthOpen && now < s.CooldownUntilMS {
		s.GoodStreak = 0
		if slow {
			s.RecoveryRequirement = RecoveryLatency
		}
		return s
	}
	if s.State == HealthHalfOpen || s.State == HealthRecovering {
		if failure {
			cooldown(HealthOpen, true)
			return s
		}
		if slow {
			cooldown(HealthDegraded, true)
			return s
		}
	} else {
		// Failures and slow TTFTs use separate rolling windows. Neither window
		// owns the lifetime of successful cold-start/recovery qualifications.
		failures := 0
		start := len(samples) - 5
		if start < 0 {
			start = 0
		}
		for _, x := range samples[start:] {
			if x.AttributableFailure || x.Timeout {
				failures++
			}
		}
		comparable, slowCount := 0, 0
		if !o.Profile.ObserveOnly() {
			for i := len(samples) - 1; i >= 0 && comparable < 5; i-- {
				x := samples[i]
				if !x.HasSemanticOutput && !x.Timeout {
					continue
				}
				comparable++
				if (x.HasSemanticOutput && x.TTFTMS > o.Profile.HealthThresholdMS) || (x.Timeout && x.TTFTMS >= o.Profile.HealthThresholdMS) {
					slowCount++
				}
			}
		}
		if slowCount >= 3 {
			s.RecoveryRequirement = RecoveryLatency
		}
		if failures >= 3 {
			cooldown(HealthOpen, false)
			return s
		}
		if slowCount >= 3 {
			cooldown(HealthDegraded, false)
			return s
		}
	}
	if failure {
		s.GoodStreak = 0
		return s
	}
	if !sameStage {
		// Late successes may be observed for latency but cannot qualify a stage
		// that did not admit their attempt. Real late failures are handled above.
		return s
	}
	qualifying := availabilitySuccess
	if s.RecoveryRequirement == RecoveryLatency {
		if !o.HasSemanticOutput {
			return s // missing latency neither advances nor erases proven fast results
		}
		qualifying = latencyFast
	}
	if !qualifying {
		s.GoodStreak = 0
		return s
	}
	if s.GoodStreak < 3 {
		s.GoodStreak++
	}
	if (s.State == HealthUnknown || s.State == HealthHalfOpen) && s.GoodStreak >= 3 {
		s.State = HealthRecovering
		s.RecoveryStage = 0
		s.ChangedAtMS = now
		s.GoodStreak = 0
		s.StageRevision++
		return s
	}
	if s.State == HealthRecovering && s.GoodStreak >= 3 && now-s.ChangedAtMS >= 30000 {
		s.RecoveryStage++
		s.GoodStreak = 0
		s.ChangedAtMS = now
		s.StageRevision++
		if s.RecoveryStage >= 2 {
			s.State = HealthHealthy
			s.RecoveryFailures = 0
			s.RecoveryRequirement = RecoveryAvailability
		}
	}
	return s
}

func RecoveryFactor(s HealthSnapshot) float64 {
	switch s.State {
	case HealthHealthy:
		return 1
	case HealthUnknown, HealthHalfOpen:
		return .1
	case HealthRecovering:
		if s.RecoveryStage <= 0 {
			return .1
		}
		if s.RecoveryStage == 1 {
			return .3
		}
		return 1
	}
	return 0
}

// ConservativeWaitScore is a routing penalty, not a TTFT quantile. Genuine
// first-output timeouts get the common cap; ordinary failures with no output
// have no observed TTFT and are excluded instead of being disguised as latency.
func ConservativeWaitScore(s HealthSnapshot, p LatencyProfile, now time.Time) (float64, bool) {
	if p.ObserveOnly() || p.HealthThresholdMS <= 0 || p.AttemptTimeoutMS <= 0 {
		return 0, false
	}
	total := float64(0)
	n := 0
	for _, x := range s.Samples {
		if x.AtMS < now.UnixMilli()-sampleWindowMS || (!x.HasSemanticOutput && !x.Timeout) {
			continue
		}
		cost := x.TTFTMS
		if x.Timeout || cost > p.AttemptTimeoutMS {
			cost = p.AttemptTimeoutMS
		}
		if cost < 0 {
			continue
		}
		total += float64(cost)
		n++
	}
	if n < 5 {
		return 0, false
	}
	return total / float64(n) / float64(p.HealthThresholdMS), true
}
