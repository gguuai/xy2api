package scheduling

import (
	"context"
	"fmt"
	"math"
	"sort"
	"time"
)

type weightedCandidate struct {
	candidate Candidate
	weight    float64
	fillOrder int
	health    HealthSnapshot
	target    float64
	effective float64
	reason    string
}
type Runtime struct{ store *RedisStore }

func NewRuntime(store *RedisStore) *Runtime { return &Runtime{store: store} }

// Evaluate is pure. Preview reads shared health but neither reserves a slot nor
// changes weighted-round-robin balances, probe leases, or retry credit.
func Evaluate(req SelectionRequest, snapshots map[int64]HealthSnapshot) (result []Decision, resultErr error) {
	if req.Policy.AccountPool {
		return evaluateAccountPool(req, snapshots)
	}
	defer func() {
		for i := range result {
			if result[i].HealthFence != nil {
				result[i].HealthFence.HealthRevision = req.Profile.HealthRevision
			}
		}
	}()
	p := NormalizePolicy(req.Policy)
	now := req.Now
	if now.IsZero() {
		now = time.Now()
	}
	cs := canonicalCandidates(p, req.Candidates, req.Attempted, req.MinPriority)
	filtered := cs[:0]
	for _, c := range cs {
		blocked := c.candidate.FailureDomain != "" && req.ExcludedFailureDomains[c.candidate.FailureDomain]
		for _, domain := range c.candidate.FailureDomains {
			if domain != "" && req.ExcludedFailureDomains[domain] {
				blocked = true
				break
			}
		}
		if blocked {
			continue
		}
		c.health = FreshHealth(snapshots[c.candidate.AccountID], now, req.Profile)
		if c.health.State == HealthOpen {
			continue
		}
		filtered = append(filtered, c)
	}
	cs = filtered
	if p.Mode == ModePin {
		var found *weightedCandidate
		for i := range cs {
			if cs[i].candidate.AccountID == p.PinAccountID {
				found = &cs[i]
				break
			}
		}
		if found != nil && found.candidate.CapacityAvailable && (found.health.State != HealthDegraded || !p.PinFallback) {
			cs = []weightedCandidate{*found}
		} else if !p.PinFallback {
			if found != nil && !found.candidate.CapacityAvailable {
				return nil, ErrCapacity
			}
			return nil, ErrNoCandidate
		} else {
			p.Mode = ModeSWRR
		}
	}
	if len(cs) == 0 {
		return nil, ErrNoCandidate
	}
	normal := func(s HealthState) bool {
		return s == HealthHealthy || s == HealthUnknown || s == HealthHalfOpen || s == HealthRecovering
	}
	highest := 0
	haveNormal := false
	for _, c := range cs {
		if normal(c.health.State) {
			highest = c.candidate.Priority
			haveNormal = true
			break
		}
	}
	overflow := false
	if haveNormal {
		capacity := false
		for _, c := range cs {
			if c.candidate.Priority == highest && normal(c.health.State) && c.candidate.CapacityAvailable {
				capacity = true
			}
		}
		if !capacity {
			if p.Overflow == OverflowWait {
				return nil, ErrCapacity
			}
			overflow = true
		}
	}
	available := make([]weightedCandidate, 0, len(cs))
	for _, c := range cs {
		if c.candidate.CapacityAvailable {
			available = append(available, c)
		}
	}
	cs = available
	if len(cs) == 0 {
		return nil, ErrCapacity
	}
	normals := make([]weightedCandidate, 0, len(cs))
	healthy := make([]weightedCandidate, 0, len(cs))
	for _, c := range cs {
		if normal(c.health.State) {
			normals = append(normals, c)
		}
		if c.health.State == HealthHealthy {
			healthy = append(healthy, c)
		}
	}
	reason := "strict_priority"
	if overflow {
		reason = "capacity_overflow"
	}
	allUnknown := true
	for _, c := range cs {
		if c.health.State != HealthUnknown {
			allUnknown = false
		}
	}
	if len(normals) > 0 && (len(healthy) > 0 || allUnknown || (p.AllDegraded == AllDegradedBestEffort && len(normals) == len(cs))) {
		top := normals[0].candidate.Priority
		primary := []weightedCandidate{}
		for _, c := range normals {
			if c.candidate.Priority == top {
				primary = append(primary, c)
			}
		}
		// Keep temporarily degraded accounts in the configured denominator so a
		// recovering low-weight account cannot consume the whole traffic pool.
		total := float64(0)
		for _, c := range cs {
			if c.candidate.Priority == top {
				total += c.weight
			}
		}
		if total <= 0 {
			for _, c := range primary {
				total += c.weight
			}
		}
		if p.Mode == ModeFillFirst || p.Mode == ModePin {
			primary = primary[:1]
			total = primary[0].weight
		}
		out := []weightedCandidate{}
		used := float64(0)
		for _, c := range primary {
			c.target = c.weight / total
			c.effective = c.target * RecoveryFactor(c.health)
			c.reason = reason
			if c.health.State != HealthHealthy {
				c.reason = "recovery_canary"
			}
			out = append(out, c)
			used += c.effective
		}
		if len(healthy) == 0 {
			// Without a healthy recipient, percentage ramp caps would deadlock. This
			// explicit best-effort branch keeps health state and concurrency safeguards.
			for i := range out {
				out[i].effective = out[i].target
				out[i].reason = "degraded_best_effort"
				if out[i].health.State == HealthUnknown {
					out[i].reason = "unknown_probe"
				}
			}
		} else if used < 1-1e-9 {
			backupPriority := healthy[0].candidate.Priority
			backup := []weightedCandidate{}
			sum := float64(0)
			for _, c := range healthy {
				if c.candidate.Priority == backupPriority {
					backup = append(backup, c)
					sum += c.weight
				}
			}
			if p.Mode == ModeFillFirst || p.Mode == ModePin {
				backup = backup[:1]
				sum = backup[0].weight
			}
			for _, b := range backup {
				extra := (1 - used) * b.weight / sum
				found := false
				for i := range out {
					if out[i].candidate.AccountID == b.candidate.AccountID {
						out[i].effective += extra
						found = true
						break
					}
				}
				if !found {
					b.effective = extra
					b.reason = "recovery_backup"
					out = append(out, b)
				}
			}
		}
		return decisions(p, out), nil
	}
	probes := make([]weightedCandidate, 0, len(cs))
	known := make([]weightedCandidate, 0, len(cs))
	for _, c := range cs {
		if probeHealth(c.health.State) {
			probes = append(probes, c)
		} else {
			known = append(known, c)
		}
	}
	if p.AllDegraded == AllDegradedFailFast {
		// Fail-fast may still spend a bounded real request to establish evidence
		// for an unknown/recovering account. It never sends known-degraded work.
		return probeOptions(p, probes)
	}
	candidates := known
	if p.AllDegraded == AllDegradedStrictPriority && len(probes) > 0 && len(known) > 0 {
		top := known[0].candidate.Priority
		ordinary := make([]weightedCandidate, 0, len(known))
		sameTierProbes := make([]weightedCandidate, 0, len(probes))
		for _, c := range known {
			if c.candidate.Priority == top {
				ordinary = append(ordinary, c)
			}
		}
		for _, c := range probes {
			if c.candidate.Priority == top {
				sameTierProbes = append(sameTierProbes, c)
			}
		}
		if len(sameTierProbes) > 0 {
			return bestEffortWithProbes(p, ordinary, sameTierProbes)
		}
	}
	if p.AllDegraded == AllDegradedBestEffort {
		best := math.Inf(1)
		scores := map[int64]float64{}
		for _, c := range known {
			v, ok := ConservativeWaitScore(c.health, req.Profile, now)
			if ok {
				scores[c.candidate.AccountID] = v
				if v < best {
					best = v
				}
			}
		}
		if len(scores) > 0 {
			candidates = nil
			for _, c := range known {
				v, ok := scores[c.candidate.AccountID]
				if ok && v <= best*1.2 {
					candidates = append(candidates, c)
				}
			}
		}
	}
	// In bounded best-effort, reserve the recovery-canary budget for
	// UNKNOWN/HALF_OPEN/RECOVERING accounts. Include probes from lower tiers as
	// control traffic so a degraded higher tier cannot starve recovery forever.
	if p.AllDegraded == AllDegradedBestEffort && len(probes) > 0 {
		if len(candidates) == 0 {
			return probeOptions(p, probes)
		}
		top := candidates[0].candidate.Priority
		ordinary := make([]weightedCandidate, 0, len(candidates))
		for _, c := range candidates {
			if c.candidate.Priority == top {
				ordinary = append(ordinary, c)
			}
		}
		if len(ordinary) == 0 {
			return probeOptions(p, probes)
		}
		return bestEffortWithProbes(p, ordinary, probes)
	}
	if len(candidates) == 0 {
		return nil, ErrNoCandidate
	}
	top := candidates[0].candidate.Priority
	chosen := []weightedCandidate{}
	total := float64(0)
	for _, c := range candidates {
		if c.candidate.Priority == top {
			chosen = append(chosen, c)
			total += c.weight
		}
	}
	if p.Mode == ModeFillFirst || p.Mode == ModePin {
		chosen = chosen[:1]
		total = chosen[0].weight
	}
	for i := range chosen {
		chosen[i].target = chosen[i].weight / total
		chosen[i].effective = chosen[i].target
		chosen[i].reason = "degraded_best_effort"
	}
	return decisions(p, chosen), nil
}

func probeHealth(s HealthState) bool {
	return s == HealthUnknown || s == HealthHalfOpen || s == HealthRecovering
}

func hasSamePriorityOption(options []Decision, selected Decision) bool {
	for _, d := range options {
		if d.AccountID != selected.AccountID && d.Priority == selected.Priority {
			return true
		}
	}
	return false
}

func probeOptions(p Policy, probes []weightedCandidate) ([]Decision, error) {
	if len(probes) == 0 {
		return nil, ErrNoCandidate
	}
	// Probe-only traffic still obeys priority. All-unknown pools are handled by
	// the normal SWRR branch above, so this path is specifically for probes
	// sharing a pool with known-degraded accounts.
	top := probes[0].candidate.Priority
	selected := make([]weightedCandidate, 0, len(probes))
	total := float64(0)
	for _, c := range probes {
		if c.candidate.Priority == top {
			selected = append(selected, c)
			total += c.weight
		}
	}
	if p.Mode == ModeFillFirst || p.Mode == ModePin {
		// Fill-first and pin are explicit single-account modes. Recovery probes
		// must not turn either mode into a weighted pool.
		selected = selected[:1]
		total = selected[0].weight
	}
	for i := range selected {
		selected[i].target = selected[i].weight / total
		selected[i].effective = selected[i].target
		selected[i].reason = "unknown_probe"
		if selected[i].health.State != HealthUnknown {
			selected[i].reason = "recovery_canary"
		}
	}
	return decisions(p, selected), nil
}

func bestEffortWithProbes(p Policy, ordinary, probes []weightedCandidate) ([]Decision, error) {
	if p.Mode == ModeFillFirst {
		all := append(append([]weightedCandidate(nil), ordinary...), probes...)
		sort.Slice(all, func(i, j int) bool {
			if all[i].candidate.Priority != all[j].candidate.Priority {
				return all[i].candidate.Priority < all[j].candidate.Priority
			}
			if all[i].fillOrder != all[j].fillOrder {
				return all[i].fillOrder < all[j].fillOrder
			}
			return all[i].candidate.AccountID < all[j].candidate.AccountID
		})
		if len(all) == 0 {
			return nil, ErrNoCandidate
		}
		chosen := all[0]
		chosen.target = 1
		chosen.effective = 1
		chosen.reason = "degraded_best_effort"
		if probeHealth(chosen.health.State) {
			chosen.reason = "unknown_probe"
			if chosen.health.State != HealthUnknown {
				chosen.reason = "recovery_canary"
			}
		}
		return decisions(p, []weightedCandidate{chosen}), nil
	}
	ordinaryTotal, probeTotal := float64(0), float64(0)
	for _, c := range ordinary {
		ordinaryTotal += c.weight
	}
	for _, c := range probes {
		probeTotal += c.weight
	}
	if ordinaryTotal <= 0 || probeTotal <= 0 {
		return nil, ErrNoCandidate
	}
	probeBudget := 0.10
	for _, c := range probes {
		if factor := RecoveryFactor(c.health); factor > probeBudget {
			probeBudget = factor
		}
	}
	if probeBudget > 1 {
		probeBudget = 1
	}
	out := make([]weightedCandidate, 0, len(ordinary)+len(probes))
	for _, c := range ordinary {
		c.target = c.weight / ordinaryTotal
		c.effective = (1 - probeBudget) * c.target
		c.reason = "degraded_best_effort"
		out = append(out, c)
	}
	for _, c := range probes {
		c.target = c.weight / probeTotal
		c.effective = probeBudget * c.target
		c.reason = "unknown_probe"
		if c.health.State != HealthUnknown {
			c.reason = "recovery_canary"
		}
		out = append(out, c)
	}
	return decisions(p, out), nil
}
func decisions(p Policy, cs []weightedCandidate) []Decision {
	out := make([]Decision, 0, len(cs))
	for _, c := range cs {
		if c.effective <= 0 {
			continue
		}
		model := c.candidate.HealthModel
		if model == "" {
			model = p.Model
		}
		out = append(out, Decision{HealthFence: &HealthFence{Model: model, Generation: c.health.Generation, StageRevision: c.health.StageRevision, HealthIdentity: c.candidate.HealthIdentity, State: c.health.State}, AccountID: c.candidate.AccountID, Priority: c.candidate.Priority, PolicyVersion: p.Version, Reason: c.reason, HealthState: c.health.State, TargetShare: c.target, EffectiveShare: c.effective, Probe: probeHealth(c.health.State)})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].AccountID < out[j].AccountID })
	return out
}
func (r *Runtime) Preview(ctx context.Context, req SelectionRequest) ([]Decision, error) {
	if r == nil || r.store == nil {
		return nil, ErrSharedState
	}
	healthRequest := req
	if req.Policy.AccountPool {
		healthRequest = accountPoolHealthRequest(req)
	}
	states, e := r.store.Snapshots(ctx, healthRequest)
	if e != nil {
		return nil, e
	}
	return Evaluate(req, states)
}
func (r *Runtime) Select(ctx context.Context, req SelectionRequest) (Decision, error) {
	if req.Policy.AccountPool {
		return r.selectAccountPool(ctx, req)
	}
	if r == nil || r.store == nil {
		return Decision{}, ErrSharedState
	}
	if req.Now.IsZero() {
		req.Now = time.Now()
	}
	policy := NormalizePolicy(req.Policy)
	if req.Attempted != nil {
		attempted := make(map[int64]bool, len(req.Attempted))
		for id, value := range req.Attempted {
			attempted[id] = value
		}
		req.Attempted = attempted
	}
	states, e := r.store.Snapshots(ctx, req)
	if e != nil {
		return Decision{}, e
	}
	pool := allocationKey(req)
	busyProbe := false
	for {
		options, e := Evaluate(req, states)
		if e != nil {
			if busyProbe && e == ErrNoCandidate {
				return Decision{}, ErrCapacity
			}
			return Decision{}, e
		}
		hasHealthyBackup := false
		for _, d := range options {
			if d.HealthState == HealthHealthy {
				hasHealthyBackup = true
				break
			}
		}
		weights := make([]allocationWeight, 0, len(options))
		for _, d := range options {
			weights = append(weights, allocationWeight{d.AccountID, d.EffectiveShare})
		}
		selected, id, e := r.store.reserve(ctx, pool, weights, req.Now)
		if e != nil {
			return Decision{}, e
		}
		var chosen Decision
		for _, d := range options {
			if d.AccountID == selected {
				chosen = d
				break
			}
		}
		chosen.ReservationID = id
		chosen.PoolKey = pool
		if !chosen.Probe {
			return chosen, nil
		}
		token, e := r.store.acquireProbe(ctx, req, selected, hasHealthyBackup)
		if e != nil {
			_ = r.store.finalize(ctx, chosen, true)
			return Decision{}, e
		}
		if token != "" {
			chosen.ProbeToken = token
			return chosen, nil
		}
		if e = r.store.finalize(ctx, chosen, true); e != nil {
			return Decision{}, e
		}
		busyProbe = true
		// A busy probe is a re-selection event only when overflow is immediate.
		// OverflowWait, pin without fallback, and fill-first retain their explicit
		// admission contract instead of silently crossing a tier/order boundary.
		if policy.Mode == ModePin && !policy.PinFallback {
			return Decision{}, ErrNoCandidate
		}
		if policy.Overflow == OverflowWait && !hasSamePriorityOption(options, chosen) && (policy.Mode != ModePin || !policy.PinFallback) {
			return Decision{}, ErrCapacity
		}
		attempted := make(map[int64]bool, len(req.Attempted)+1)
		for accountID, already := range req.Attempted {
			attempted[accountID] = already
		}
		attempted[selected] = true
		req.Attempted = attempted
	}
}
func (r *Runtime) CommitSelection(ctx context.Context, d Decision) error {
	if r == nil || r.store == nil {
		return ErrSharedState
	}
	return r.store.finalize(ctx, d, false)
}
func (r *Runtime) ReleaseSelection(ctx context.Context, d Decision) error {
	if r == nil || r.store == nil {
		return ErrSharedState
	}
	e := r.store.finalize(ctx, d, true)
	p := r.store.ReleaseProbe(ctx, d.ProbeToken)
	if e != nil {
		return e
	}
	return p
}
func (r *Runtime) ReleaseProbe(ctx context.Context, token string) error {
	if r == nil || r.store == nil {
		return ErrSharedState
	}
	return r.store.ReleaseProbe(ctx, token)
}
func (r *Runtime) Observe(ctx context.Context, o Observation) error {
	if r == nil || r.store == nil {
		return ErrSharedState
	}
	return r.store.Observe(ctx, o)
}
func (r *Runtime) AcquireDispatchBudget(ctx context.Context, p Policy, id string, retry bool) (BudgetReservation, error) {
	if p.AccountPool {
		// Account pools use the visible per-request ledger and authoritative
		// dispatch/capacity gates, not a second hidden cross-request quota.
		return BudgetReservation{}, nil
	}
	if r == nil || r.store == nil {
		return BudgetReservation{}, ErrSharedState
	}
	return r.store.AcquireDispatchBudget(ctx, p, id, retry)
}
func (r *Runtime) RefundDispatchBudget(ctx context.Context, b BudgetReservation) error {
	if r == nil || r.store == nil {
		return ErrSharedState
	}
	return r.store.RefundDispatchBudget(ctx, b)
}
func (r *Runtime) CommitDispatchBudget(ctx context.Context, b BudgetReservation) error {
	if r == nil || r.store == nil {
		return ErrSharedState
	}
	return r.store.CommitDispatchBudget(ctx, b)
}

// Explain returns the same pure evaluated options used for dispatch.
func (r *Runtime) Explain(ctx context.Context, req SelectionRequest) ([]Decision, error) {
	return r.Preview(ctx, req)
}
func (d Decision) String() string {
	return fmt.Sprintf("account=%d priority=%d reason=%s policy=%d", d.AccountID, d.Priority, d.Reason, d.PolicyVersion)
}

func (r *Runtime) CanRetry(ctx context.Context, p Policy) (bool, error) {
	if p.AccountPool {
		return true, nil
	}
	if r == nil || r.store == nil {
		return false, ErrSharedState
	}
	return r.store.CanRetry(ctx, p)
}

func (r *Runtime) ForgetDispatchReceipts(ctx context.Context, d Decision, b BudgetReservation) error {
	if r == nil || r.store == nil {
		return ErrSharedState
	}
	return r.store.ForgetDispatchReceipts(ctx, d, b)
}
