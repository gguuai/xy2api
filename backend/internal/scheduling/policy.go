package scheduling

import (
	"fmt"
	"sort"
	"strings"
)

// NormalizeReasoningLabel is shared by live dispatch and explain so an
// omitted reasoning effort resolves to the same profile key everywhere.
func NormalizeReasoningLabel(raw string) string {
	label := strings.ToLower(strings.TrimSpace(raw))
	if label == "" {
		return "default"
	}
	return label
}

func DefaultRetryPolicy() RetryPolicy {
	return RetryPolicy{Mode: "bounded_same_tier_first", MaxAttempts: 3, MaxPerTier: 2, MaxPerAccount: 1, MaxAfterTimeout: 1, InitialPerToken: 10, Burst: 2, SwitchMarginMS: 200, ReserveFallback: true, CrossTier: true}
}

func NormalizePolicy(p Policy) Policy {
	if p.Mode == "" {
		p.Mode = ModeSWRR
	}
	if p.Overflow == "" {
		p.Overflow = OverflowImmediate
	}
	if p.AllDegraded == "" {
		p.AllDegraded = AllDegradedBestEffort
	}
	if p.Retry == (RetryPolicy{}) {
		p.Retry = DefaultRetryPolicy()
	} else {
		if p.Retry.Mode == "" {
			p.Retry.Mode = "bounded_same_tier_first"
		}
		if p.Retry.MaxAttempts == 0 {
			p.Retry.MaxAttempts = 3
		}
		if p.Retry.MaxPerTier == 0 {
			p.Retry.MaxPerTier = 2
		}
		if p.Retry.MaxPerAccount == 0 {
			p.Retry.MaxPerAccount = 1
		}
		if p.Retry.InitialPerToken == 0 {
			p.Retry.InitialPerToken = 10
		}
		if p.Retry.Burst == 0 {
			p.Retry.Burst = 2
		}
	}
	return p
}

func ValidatePolicy(raw Policy) error {
	p := NormalizePolicy(raw)
	if strings.TrimSpace(p.Model) == "" {
		return fmt.Errorf("model is required")
	}
	if p.GroupID < 0 || p.Version < 0 {
		return fmt.Errorf("negative group or version")
	}
	switch p.Mode {
	case ModeSWRR, ModePin, ModeFillFirst:
	default:
		return fmt.Errorf("invalid mode %q", p.Mode)
	}
	if p.Mode == ModePin && p.PinAccountID <= 0 {
		return fmt.Errorf("pin requires pin_account_id")
	}
	if p.Overflow != OverflowImmediate && p.Overflow != OverflowWait {
		return fmt.Errorf("invalid overflow")
	}
	if p.QueueWaitMS < 0 || p.QueueWaitMS > 60000 {
		return fmt.Errorf("queue_wait_ms must be within 0..60000")
	}
	switch p.AllDegraded {
	case AllDegradedBestEffort, AllDegradedStrictPriority, AllDegradedFailFast:
	default:
		return fmt.Errorf("invalid all_degraded")
	}
	seen := map[int64]bool{}
	for _, a := range p.Accounts {
		if a.AccountID <= 0 || seen[a.AccountID] {
			return fmt.Errorf("invalid or duplicate account_id %d", a.AccountID)
		}
		seen[a.AccountID] = true
		if a.Weight < 0 || a.Weight > 1000000 {
			return fmt.Errorf("weight must be within 0..1000000")
		}
	}
	names := map[string]bool{}
	for _, v := range p.Profiles {
		if v.Name == "" || names[v.Name] {
			return fmt.Errorf("profile names must be nonempty and unique")
		}
		names[v.Name] = true
		if v.ContextMinTokens < 0 || (v.ContextMaxTokens > 0 && v.ContextMaxTokens <= v.ContextMinTokens) {
			return fmt.Errorf("invalid context range")
		}
		if v.ObserveOnly() {
			continue
		}
		if v.RecoveryThresholdMS <= 0 || v.RecoveryThresholdMS >= v.HealthThresholdMS || v.HealthThresholdMS >= v.AttemptTimeoutMS || v.AttemptTimeoutMS > v.TotalBudgetMS || v.MinAttemptWindowMS <= 0 || v.MinAttemptWindowMS > v.AttemptTimeoutMS {
			return fmt.Errorf("profile %s requires 0<R<H<T<=D and 0<M<=T", v.Name)
		}
		if v.TotalBudgetMS > 1800000 {
			return fmt.Errorf("profile total budget exceeds 30 minutes")
		}
	}
	r := p.Retry
	if r.Mode != "bounded_same_tier_first" && r.Mode != "exhaust_same_tier" {
		return fmt.Errorf("invalid retry mode")
	}
	if r.MaxAttempts < 1 || r.MaxAttempts > 10 || r.MaxPerTier < 1 || r.MaxPerTier > r.MaxAttempts || r.MaxPerAccount < 1 || r.MaxPerAccount > r.MaxAttempts || r.MaxAfterTimeout < 0 || r.MaxAfterTimeout > 1 || r.InitialPerToken < 1 || r.Burst < 1 || r.Burst > 100 || r.SwitchMarginMS < 0 {
		return fmt.Errorf("invalid retry budget")
	}
	return nil
}

func (p LatencyProfile) ObserveOnly() bool {
	return p.HealthThresholdMS == 0 && p.RecoveryThresholdMS == 0 && p.AttemptTimeoutMS == 0 && p.TotalBudgetMS == 0 && p.MinAttemptWindowMS == 0
}

// ResolveProfile chooses the most specific explicit matching override. Context
// ranges are lower-inclusive, upper-exclusive; a zero upper bound is unlimited.
func ResolveProfile(p Policy, reasoning string, contextTokens int64) (LatencyProfile, bool) {
	return ResolveProfileForTransport(p, reasoning, contextTokens, "")
}
func ResolveProfileForTransport(p Policy, reasoning string, contextTokens int64, transport string) (LatencyProfile, bool) {
	transport = CanonicalTransport(transport)
	var best LatencyProfile
	bestScore := -1
	for _, v := range p.Profiles {
		v.Transport = CanonicalTransport(v.Transport)
		if v.Reasoning != "" && v.Reasoning != reasoning {
			continue
		}
		if v.Transport != "" && v.Transport != transport {
			continue
		}
		if contextTokens < 0 && (v.ContextMinTokens > 0 || v.ContextMaxTokens > 0) {
			continue
		}
		if contextTokens >= 0 && (contextTokens < v.ContextMinTokens || (v.ContextMaxTokens > 0 && contextTokens >= v.ContextMaxTokens)) {
			continue
		}
		score := 0
		if v.Reasoning != "" {
			score += 4
		}
		if v.Transport != "" {
			score += 2
		}
		if v.ContextMinTokens > 0 || v.ContextMaxTokens > 0 {
			score++
		}
		if score > bestScore {
			best = v
			bestScore = score
		}
	}
	return best, bestScore >= 0
}
func ContextBucket(tokens int64) string {
	if tokens < 0 {
		return "unknown"
	}
	if tokens < 8192 {
		return "0_8k"
	}
	if tokens < 32768 {
		return "8k_32k"
	}
	return "32k_plus"
}

func accountRule(p Policy, c Candidate) AccountRule {
	r := AccountRule{AccountID: c.AccountID, Weight: 1}
	for _, v := range p.Accounts {
		if v.AccountID == c.AccountID {
			return v
		}
	}
	return r
}
func canonicalCandidates(p Policy, cs []Candidate, attempted map[int64]bool, minPriority *int) []weightedCandidate {
	out := make([]weightedCandidate, 0, len(cs))
	seen := map[int64]bool{}
	for _, c := range cs {
		if c.AccountID <= 0 || seen[c.AccountID] || !c.HardEligible || attempted[c.AccountID] {
			continue
		}
		seen[c.AccountID] = true
		r := accountRule(p, c)
		if r.Weight <= 0 {
			continue
		}
		if r.Priority != nil {
			c.Priority = *r.Priority
		}
		if minPriority != nil && c.Priority < *minPriority {
			continue
		}
		out = append(out, weightedCandidate{candidate: c, weight: float64(r.Weight), fillOrder: r.FillOrder})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].candidate.Priority != out[j].candidate.Priority {
			return out[i].candidate.Priority < out[j].candidate.Priority
		}
		if out[i].fillOrder != out[j].fillOrder {
			return out[i].fillOrder < out[j].fillOrder
		}
		return out[i].candidate.AccountID < out[j].candidate.AccountID
	})
	return out
}
