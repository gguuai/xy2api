package scheduling

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"
)

// AccountPoolProfileName identifies the internal waiting budget and availability
// circuit. Account pools have no per-model policies or latency-score profiles.
const AccountPoolProfileName = "account_pool"

// evaluateAccountPool has exactly three routing steps: admission, highest
// available priority, and the configured weight within that priority. Model
// support and authoritative pause/failure gates are supplied as HardEligible.
func evaluateAccountPool(req SelectionRequest, states map[int64]HealthSnapshot) ([]Decision, error) {
	now := req.Now
	if now.IsZero() {
		now = time.Now()
	}
	p := req.Policy
	cs := accountPoolCandidates(req)
	eligible := make([]weightedCandidate, 0, len(cs))
	for _, c := range cs {
		if accountPoolDomainExcluded(c.candidate, req.ExcludedFailureDomains) {
			continue
		}
		c.health = freshAccountPoolHealth(states[c.candidate.AccountID], now)
		if c.health.State == HealthOpen {
			continue
		}
		eligible = append(eligible, c)
	}
	if p.Mode == ModePin {
		for _, c := range eligible {
			if c.candidate.AccountID != p.PinAccountID {
				continue
			}
			if c.candidate.CapacityAvailable {
				c.target, c.effective, c.reason = 1, 1, "protocol_owner"
				return accountPoolDecisions(req, []weightedCandidate{c}), nil
			}
			if !p.PinFallback {
				return nil, ErrCapacity
			}
		}
		if !p.PinFallback {
			return nil, ErrNoCandidate
		}
	}
	if len(eligible) == 0 {
		return nil, ErrNoCandidate
	}
	top := eligible[0].candidate.Priority
	chosen := make([]weightedCandidate, 0, len(eligible))
	total := float64(0)
	for _, c := range eligible {
		if !c.candidate.CapacityAvailable {
			continue
		}
		if len(chosen) == 0 {
			top = c.candidate.Priority
		}
		if c.candidate.Priority != top {
			break
		}
		chosen = append(chosen, c)
		total += c.weight
	}
	if len(chosen) == 0 {
		return nil, ErrCapacity
	}
	for i := range chosen {
		chosen[i].target = chosen[i].weight / total
		chosen[i].effective = chosen[i].target
		chosen[i].reason = "account_priority"
		if top != eligible[0].candidate.Priority {
			chosen[i].reason = "capacity_overflow"
		}
		if chosen[i].health.State == HealthHalfOpen {
			chosen[i].reason = "recovery_probe"
		}
	}
	return accountPoolDecisions(req, chosen), nil
}

func accountPoolDomainExcluded(c Candidate, excluded map[string]bool) bool {
	if c.FailureDomain != "" && excluded[c.FailureDomain] {
		return true
	}
	for _, key := range c.FailureDomains {
		if key != "" && excluded[key] {
			return true
		}
	}
	return false
}

func accountPoolRules(p Policy) map[int64]AccountRule {
	rules := make(map[int64]AccountRule, len(p.Accounts))
	for _, rule := range p.Accounts {
		if _, exists := rules[rule.AccountID]; !exists {
			rules[rule.AccountID] = rule
		}
	}
	return rules
}

// Rules are sparse: new group members inherit their account priority and weight
// one until configured. Membership itself comes only from the candidate source.
// A map lookup avoids scanning every rule for every account on the hot path.
func accountPoolCandidates(req SelectionRequest) []weightedCandidate {
	rules := accountPoolRules(req.Policy)
	seen := make(map[int64]bool, len(req.Candidates))
	cs := make([]weightedCandidate, 0, len(req.Candidates))
	for _, c := range req.Candidates {
		if c.AccountID <= 0 || seen[c.AccountID] || !c.HardEligible || req.Attempted[c.AccountID] {
			continue
		}
		seen[c.AccountID] = true
		weight := int64(1)
		if rule, ok := rules[c.AccountID]; ok {
			weight = rule.Weight
			if rule.Priority != nil {
				c.Priority = *rule.Priority
			}
		}
		if weight <= 0 || (req.MinPriority != nil && c.Priority < *req.MinPriority) {
			continue
		}
		cs = append(cs, weightedCandidate{candidate: c, weight: float64(weight)})
	}
	sort.Slice(cs, func(i, j int) bool {
		if cs[i].candidate.Priority != cs[j].candidate.Priority {
			return cs[i].candidate.Priority < cs[j].candidate.Priority
		}
		return cs[i].candidate.AccountID < cs[j].candidate.AccountID
	})
	return cs
}

// Do not read health for accounts that cannot take this request. Otherwise an
// irrelevant account's corrupt cache could fail a healthy, supported pool.
func accountPoolHealthRequest(req SelectionRequest) SelectionRequest {
	candidates := accountPoolCandidates(req)
	out := make([]Candidate, 0, len(candidates))
	for _, item := range candidates {
		c := item.candidate
		if !c.CapacityAvailable || accountPoolDomainExcluded(c, req.ExcludedFailureDomains) {
			continue
		}
		if req.Policy.Mode == ModePin && !req.Policy.PinFallback && c.AccountID != req.Policy.PinAccountID {
			continue
		}
		out = append(out, c)
	}
	req.Candidates = out
	return req
}

func accountPoolDecisions(req SelectionRequest, cs []weightedCandidate) []Decision {
	out := decisions(req.Policy, cs)
	for i := range out {
		out[i].Probe = out[i].HealthState == HealthHalfOpen
		out[i].HealthFence.HealthRevision = req.Profile.HealthRevision
	}
	return out
}

// The allocation identity is a group and an eligible priority membership, not
// a model/profile. Equal support sets share fairness; alternating support sets
// do not reset each other's balances. Weight edits get a separate identity, so
// in-flight old policy versions/refunds cannot reset the new pool's balances.
func accountPoolAllocationKey(req SelectionRequest, options []Decision) string {
	type member struct {
		ID     int64
		Weight int64
	}
	ids := make([]member, 0, len(options))
	rules := accountPoolRules(req.Policy)
	for _, d := range options {
		weight := int64(1)
		if rule, ok := rules[d.AccountID]; ok {
			weight = rule.Weight
		}
		ids = append(ids, member{d.AccountID, weight})
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i].ID < ids[j].ID })
	tier := 0
	if len(options) > 0 {
		tier = options[0].Priority
	}
	kind := "initial"
	if req.Retry {
		kind = "retry"
	}
	if req.Policy.Mode == ModePin {
		kind = "owner:" + strconv.FormatInt(req.Policy.PinAccountID, 10)
	}
	raw, _ := json.Marshal([]any{"account_pool_v1", req.Policy.GroupID, tier, kind, ids})
	return "xy2:scheduling:{" + digest(string(raw)) + "}"
}

// Probe occupancy follows the actual upstream model/credential identity, even
// when two client aliases or groups refer to the same account capability.
func probePoolKey(req SelectionRequest, accountID int64) string {
	if !req.Policy.AccountPool && req.Profile.Name != AccountPoolProfileName {
		return "xy2:scheduling:probe:{" + profileKey(req.Policy.Model, req.Profile, req.Reasoning, req.ContextBucket, req.Transport) + "}"
	}
	model, identity := req.Policy.Model, ""
	for _, c := range req.Candidates {
		if c.AccountID == accountID {
			if c.HealthModel != "" {
				model = c.HealthModel
			}
			identity = c.HealthIdentity
			break
		}
	}
	// Keep health and its lease in one Redis Cluster slot. Final health
	// admission can then WATCH the generation and lease owner atomically.
	return accountPoolProbePrefix(accountID, model, req.Profile, req.Reasoning, req.ContextBucket, req.Transport, identity)
}

func accountPoolProbePrefix(accountID int64, model string, profile LatencyProfile, reasoning, bucket, transport, identity string) string {
	health := HealthRedisKey(accountID, model, profile, reasoning, bucket, transport, identity)
	return strings.Replace(health, "xy2:scheduling:health:", "xy2:scheduling:probe:", 1)
}

func accountPoolLocalRequest(req SelectionRequest) SelectionRequest {
	if req.Now.IsZero() {
		req.Now = time.Now()
	}
	attempted := make(map[int64]bool, len(req.Attempted)+1)
	for id, value := range req.Attempted {
		attempted[id] = value
	}
	req.Attempted = attempted
	return req
}

func (r *Runtime) selectAccountPool(ctx context.Context, req SelectionRequest) (Decision, error) {
	if r == nil || r.store == nil || r.store.client == nil {
		return Decision{}, ErrSharedState
	}
	req = accountPoolLocalRequest(req)
	states, err := r.store.Snapshots(ctx, accountPoolHealthRequest(req))
	if err != nil {
		return Decision{}, err
	}
	busy := false
	for {
		options, err := evaluateAccountPool(req, states)
		if err != nil {
			if busy && err == ErrNoCandidate {
				err = ErrCapacity
			}
			return Decision{}, err
		}
		pool := accountPoolAllocationKey(req, options)
		weights := make([]allocationWeight, 0, len(options))
		for _, d := range options {
			weights = append(weights, allocationWeight{d.AccountID, d.EffectiveShare})
		}
		selected, receipt, err := r.store.reserve(ctx, pool, weights, req.Now)
		if err != nil {
			return Decision{}, err
		}
		var chosen Decision
		for _, d := range options {
			if d.AccountID == selected {
				chosen = d
				break
			}
		}
		chosen.ReservationID, chosen.PoolKey = receipt, pool
		if chosen.AccountID == 0 {
			_ = r.store.finalize(ctx, chosen, true)
			return Decision{}, fmt.Errorf("%w: allocation returned an unknown account", ErrSharedState)
		}
		if !chosen.Probe {
			return chosen, nil
		}
		token, err := r.store.acquireProbe(ctx, req, selected, false)
		if err != nil {
			_ = r.store.finalize(ctx, chosen, true)
			return Decision{}, err
		}
		if token != "" {
			chosen.ProbeToken = token
			return chosen, nil
		}
		if err = r.store.finalize(ctx, chosen, true); err != nil {
			return Decision{}, err
		}
		if req.Policy.Mode == ModePin && !req.Policy.PinFallback {
			return Decision{}, ErrCapacity
		}
		req.Attempted[selected] = true
		busy = true
	}
}

func (r *Runtime) inspectAccountPool(ctx context.Context, req SelectionRequest) (SelectionIntrospection, error) {
	req = accountPoolLocalRequest(req)
	out := SelectionIntrospection{SnapshotAt: req.Now, Health: map[int64]HealthSnapshot{}}
	if r == nil || r.store == nil || r.store.client == nil {
		return out, ErrSharedState
	}
	healthRequest := accountPoolHealthRequest(req)
	states, err := r.store.Snapshots(ctx, healthRequest)
	if err != nil {
		return out, err
	}
	for _, c := range healthRequest.Candidates {
		out.Health[c.AccountID] = freshAccountPoolHealth(states[c.AccountID], req.Now)
	}
	busy := false
	for {
		options, err := evaluateAccountPool(req, states)
		if err != nil {
			if busy && err == ErrNoCandidate {
				err = ErrCapacity
			}
			return out, err
		}
		out.Options = options
		state, err := r.store.inspectAllocation(ctx, accountPoolAllocationKey(req, options), req.Now)
		if err != nil {
			return out, err
		}
		chosen, err := state.peek(options)
		if err != nil {
			return out, err
		}
		if !chosen.Probe {
			out.Selected = &chosen
			return out, nil
		}
		available, err := r.store.inspectProbeAvailable(ctx, req, chosen.AccountID, false)
		if err != nil {
			return out, err
		}
		if available {
			out.Selected = &chosen
			return out, nil
		}
		if req.Policy.Mode == ModePin && !req.Policy.PinFallback {
			return out, ErrCapacity
		}
		req.Attempted[chosen.AccountID] = true
		busy = true
	}
}
