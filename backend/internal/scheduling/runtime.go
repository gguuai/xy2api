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
func Evaluate(req SelectionRequest, snapshots map[int64]HealthSnapshot) ([]Decision, error) {
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
			break
		}
	}
	if len(normals) > 0 && (len(healthy) > 0 || allUnknown) {
		top := normals[0].candidate.Priority
		primary := []weightedCandidate{}
		for _, c := range normals {
			if c.candidate.Priority == top {
				primary = append(primary, c)
			}
		}
		total := float64(0)
		for _, c := range primary {
			total += c.weight
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
	if p.AllDegraded == AllDegradedFailFast {
		return nil, ErrNoCandidate
	}
	candidates := cs
	if p.AllDegraded == AllDegradedBestEffort {
		best := math.Inf(1)
		scores := map[int64]float64{}
		for _, c := range cs {
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
			for _, c := range cs {
				v, ok := scores[c.candidate.AccountID]
				if ok && v <= best*1.2 {
					candidates = append(candidates, c)
				}
			}
		}
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
func decisions(p Policy, cs []weightedCandidate) []Decision {
	out := make([]Decision, 0, len(cs))
	for _, c := range cs {
		if c.effective <= 0 {
			continue
		}
		out = append(out, Decision{AccountID: c.candidate.AccountID, Priority: c.candidate.Priority, PolicyVersion: p.Version, Reason: c.reason, HealthState: c.health.State, TargetShare: c.target, EffectiveShare: c.effective, Probe: c.health.State == HealthUnknown || c.health.State == HealthHalfOpen})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].AccountID < out[j].AccountID })
	return out
}
func (r *Runtime) Preview(ctx context.Context, req SelectionRequest) ([]Decision, error) {
	if r == nil || r.store == nil {
		return nil, ErrSharedState
	}
	states, e := r.store.Snapshots(ctx, req)
	if e != nil {
		return nil, e
	}
	return Evaluate(req, states)
}
func (r *Runtime) Select(ctx context.Context, req SelectionRequest) (Decision, error) {
	if r == nil || r.store == nil {
		return Decision{}, ErrSharedState
	}
	if req.Now.IsZero() {
		req.Now = time.Now()
	}
	states, e := r.store.Snapshots(ctx, req)
	if e != nil {
		return Decision{}, e
	}
	options, e := Evaluate(req, states)
	if e != nil {
		return Decision{}, e
	}
	pool := allocationKey(req)
	hasHealthyBackup := false
	for _, d := range options {
		if d.HealthState == HealthHealthy {
			hasHealthyBackup = true
			break
		}
	}
	for len(options) > 0 {
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
		next := options[:0]
		for _, d := range options {
			if d.AccountID != selected {
				next = append(next, d)
			}
		}
		options = next
	}
	return Decision{}, ErrCapacity
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
