package scheduling

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"math/big"
	"strconv"
	"strings"
	"time"
)

type SelectionIntrospection struct {
	Options    []Decision               `json:"options"`
	Health     map[int64]HealthSnapshot `json:"health"`
	Selected   *Decision                `json:"selected"`
	SnapshotAt time.Time                `json:"snapshot_at"`
}

// The Lua invocation executes only read commands. It gives scores and outstanding
// reservations one atomic snapshot without cleanup, reservation, or balance writes.
const inspectAllocationLua = `
return {redis.call('HGETALL',KEYS[1]),redis.call('HGETALL',KEYS[2]),redis.call('ZRANGEBYSCORE',KEYS[3],'-inf',ARGV[1]),redis.call('ZCARD',KEYS[3])}
`

type allocationInspection struct {
	signature    string
	scores       map[int64]string
	reservations map[string]string
	expired      []string
	pending      int64
}

func (s *RedisStore) inspectAllocation(ctx context.Context, pool string, now time.Time) (allocationInspection, error) {
	v := allocationInspection{scores: map[int64]string{}, reservations: map[string]string{}}
	raw, err := s.client.Eval(ctx, inspectAllocationLua, []string{pool + ":scores", pool + ":reservations", pool + ":expiry"}, now.UnixMilli()).Slice()
	if err != nil {
		return v, fmt.Errorf("%w: %v", ErrSharedState, err)
	}
	if len(raw) != 4 {
		return v, ErrSharedState
	}
	scores, ok := raw[0].([]interface{})
	if !ok {
		return v, ErrSharedState
	}
	for i := 0; i+1 < len(scores); i += 2 {
		key := fmt.Sprint(scores[i])
		val := fmt.Sprint(scores[i+1])
		if key == "__sig" {
			v.signature = val
			continue
		}
		id, e := strconv.ParseInt(key, 10, 64)
		if e != nil {
			return v, ErrSharedState
		}
		score, e := strconv.ParseFloat(val, 64)
		if e != nil || math.IsNaN(score) || math.IsInf(score, 0) {
			return v, ErrSharedState
		}
		v.scores[id] = val
	}
	reservations, ok := raw[1].([]interface{})
	if !ok {
		return v, ErrSharedState
	}
	for i := 0; i+1 < len(reservations); i += 2 {
		v.reservations[fmt.Sprint(reservations[i])] = fmt.Sprint(reservations[i+1])
	}
	expired, ok := raw[2].([]interface{})
	if !ok {
		return v, ErrSharedState
	}
	for _, id := range expired {
		v.expired = append(v.expired, fmt.Sprint(id))
	}
	v.pending, err = strconv.ParseInt(fmt.Sprint(raw[3]), 10, 64)
	return v, err
}
func (v *allocationInspection) cleanupExpired() error {
	n := len(v.expired)
	if n > 100 {
		n = 100
	}
	for _, id := range v.expired[:n] {
		if raw, ok := v.reservations[id]; ok {
			var old struct {
				Signature string `json:"sig"`
				Weights   []struct {
					ID     int64   `json:"id"`
					Weight float64 `json:"w"`
				} `json:"weights"`
				Selected int64   `json:"selected"`
				Total    float64 `json:"total"`
			}
			if err := json.Unmarshal([]byte(raw), &old); err != nil {
				return ErrSharedState
			}
			if old.Signature == v.signature {
				for _, w := range old.Weights {
					v.scores[w.ID] = inspectionIncrement(v.scores[w.ID], -w.Weight)
				}
				v.scores[old.Selected] = inspectionIncrement(v.scores[old.Selected], old.Total)
			}
			delete(v.reservations, id)
		}
		v.pending--
	}
	v.expired = v.expired[n:]
	if v.pending >= 1024 {
		return fmt.Errorf("%w: too many pending reservations", ErrSharedState)
	}
	return nil
}
func (v *allocationInspection) peek(options []Decision) (Decision, error) {
	if err := v.cleanupExpired(); err != nil {
		return Decision{}, err
	}
	wire := make([]map[string]any, 0, len(options))
	for _, d := range options {
		wire = append(wire, map[string]any{"id": d.AccountID, "w": d.EffectiveShare})
	}
	raw, err := json.Marshal(wire)
	if err != nil {
		return Decision{}, err
	}
	signature := digest(string(raw))
	if v.signature != signature {
		v.signature = signature
		v.scores = map[int64]string{}
	}
	var chosen Decision
	var best float64
	first := true
	for _, d := range options {
		current, _ := strconv.ParseFloat(inspectionIncrement(v.scores[d.AccountID], d.EffectiveShare), 64)
		if first || current > best {
			chosen = d
			best = current
			first = false
		}
	}
	if first {
		return Decision{}, ErrNoCandidate
	}
	return chosen, nil
}
func (s *RedisStore) inspectProbeAvailable(ctx context.Context, req SelectionRequest, id int64, shared bool) (bool, error) {
	key := "xy2:scheduling:probe:{" + profileKey(req.Policy.Model, req.Profile, req.Reasoning, req.ContextBucket, req.Transport) + "}"
	keys := []string{key + ":account:" + strconv.FormatInt(id, 10)}
	if shared {
		keys = append(keys, key+":pool")
	}
	n, err := s.client.Exists(ctx, keys...).Result()
	if err != nil {
		return false, fmt.Errorf("%w: %v", ErrSharedState, err)
	}
	return n == 0, nil
}

// InspectSelection predicts the next physical-selection candidate from current
// shared balances. It does not guarantee admission after the snapshot changes.
func (r *Runtime) InspectSelection(ctx context.Context, req SelectionRequest) (SelectionIntrospection, error) {
	out := SelectionIntrospection{SnapshotAt: req.Now}
	if out.SnapshotAt.IsZero() {
		out.SnapshotAt = time.Now()
	}
	req.Now = out.SnapshotAt
	if r == nil || r.store == nil || r.store.client == nil {
		return out, ErrSharedState
	}
	states, err := r.store.Snapshots(ctx, req)
	if err != nil {
		return out, err
	}
	out.Health = map[int64]HealthSnapshot{}
	for _, candidate := range req.Candidates {
		out.Health[candidate.AccountID] = FreshHealth(states[candidate.AccountID], req.Now, req.Profile)
	}
	out.Options, err = Evaluate(req, states)
	if err != nil {
		return out, err
	}
	state, err := r.store.inspectAllocation(ctx, allocationKey(req), req.Now)
	if err != nil {
		return out, err
	}
	policy := NormalizePolicy(req.Policy)
	if req.Attempted != nil {
		attempted := make(map[int64]bool, len(req.Attempted))
		for id, value := range req.Attempted {
			attempted[id] = value
		}
		req.Attempted = attempted
	}
	options := append([]Decision(nil), out.Options...)
	busyProbe := false
	for {
		// Select re-evaluates after a busy probe. Explain follows the same pure
		// transition so it predicts lower-tier overflow without reserving state.
		if len(options) == 0 {
			var e error
			options, e = Evaluate(req, states)
			if e != nil {
				if busyProbe && e == ErrNoCandidate {
					return out, ErrCapacity
				}
				return out, e
			}
		}
		chosen, e := state.peek(options)
		if e != nil {
			return out, e
		}
		if !chosen.Probe {
			out.Selected = &chosen
			return out, nil
		}
		sharedProbe := false
		for _, d := range options {
			if d.HealthState == HealthHealthy {
				sharedProbe = true
				break
			}
		}
		available, e := r.store.inspectProbeAvailable(ctx, req, chosen.AccountID, sharedProbe)
		if e != nil {
			return out, e
		}
		if available {
			out.Selected = &chosen
			return out, nil
		}
		if policy.Mode == ModePin && !policy.PinFallback {
			return out, ErrNoCandidate
		}
		if policy.Overflow == OverflowWait && !hasSamePriorityOption(options, chosen) && !(policy.Mode == ModePin && policy.PinFallback) {
			return out, ErrCapacity
		}
		if req.Attempted == nil {
			req.Attempted = map[int64]bool{}
		}
		req.Attempted[chosen.AccountID] = true
		busyProbe = true
		options = nil
	}
}

// NextRecovery cannot revive paused, zero-weight, excluded or already-tried accounts.
func (r *Runtime) NextRecovery(ctx context.Context, req SelectionRequest) (time.Time, error) {
	if r == nil || r.store == nil {
		return time.Time{}, ErrSharedState
	}
	if req.Now.IsZero() {
		req.Now = time.Now()
	}
	snapshots, err := r.store.Snapshots(ctx, req)
	if err != nil {
		return time.Time{}, err
	}
	var next time.Time
	for _, c := range canonicalCandidates(NormalizePolicy(req.Policy), req.Candidates, req.Attempted, req.MinPriority) {
		if req.Policy.Mode == ModePin && !req.Policy.PinFallback && c.candidate.AccountID != req.Policy.PinAccountID {
			continue
		}
		blocked := c.candidate.FailureDomain != "" && req.ExcludedFailureDomains[c.candidate.FailureDomain]
		for _, domain := range c.candidate.FailureDomains {
			if domain != "" && req.ExcludedFailureDomains[domain] {
				blocked = true
			}
		}
		if blocked {
			continue
		}
		h := FreshHealth(snapshots[c.candidate.AccountID], req.Now, req.Profile)
		if h.State != HealthOpen || h.CooldownUntilMS <= req.Now.UnixMilli() {
			continue
		}
		at := time.UnixMilli(h.CooldownUntilMS)
		if next.IsZero() || at.Before(next) {
			next = at
		}
	}
	return next, nil
}

// Redis HINCRBYFLOAT formats the decimal sum before Lua converts it to double.
// Native float addition would turn -0.2+0.7 into 0.49999999999999994 and break
// stable SWRR ties. Decimal addition reproduces that command boundary.
func inspectionIncrement(value string, delta float64) string {
	if value == "" {
		value = "0"
	}
	a, ok := new(big.Rat).SetString(value)
	if !ok {
		return "NaN"
	}
	b, ok := new(big.Rat).SetString(strconv.FormatFloat(delta, 'g', -1, 64))
	if !ok {
		return "NaN"
	}
	a.Add(a, b)
	out := strings.TrimRight(strings.TrimRight(a.FloatString(17), "0"), ".")
	if out == "-0" {
		return "0"
	}
	return out
}

// CanRetryAfterDispatch runs before this dispatch reserves its credit. A retry
// needs one token for itself plus one for the fallback. Initial dispatch can earn
// one credit only if this logical request has not already claimed its initial key.
func (r *Runtime) CanRetryAfterDispatch(ctx context.Context, p Policy, logicalID string, currentRetry bool) (bool, error) {
	if r == nil || r.store == nil || r.store.client == nil {
		return false, ErrSharedState
	}
	p = NormalizePolicy(p)
	key := "xy2:scheduling:{" + digest(scopeKey(p)+":retry_budget") + "}"
	mode := "initial"
	if currentRetry {
		mode = "retry"
	}
	canMint := "no"
	if logicalID != "" {
		canMint = "yes"
	}
	n, err := r.store.client.Eval(ctx, inspectRetryAfterDispatchLua, []string{key + ":credit", key + ":initial:" + digest(logicalID)}, p.Retry.InitialPerToken, p.Retry.Burst, mode, canMint).Int()
	if err != nil {
		return false, fmt.Errorf("%w: %v", ErrSharedState, err)
	}
	return n == 1, nil
}

const inspectRetryAfterDispatchLua = `
local ratio,burst=tonumber(ARGV[1]),tonumber(ARGV[2]);local maximum=ratio*burst
local credit=math.min(maximum,tonumber(redis.call('GET',KEYS[1]) or tostring(maximum)))
if ARGV[3]=='retry' then credit=credit-ratio
elseif ARGV[4]=='yes' and redis.call('EXISTS',KEYS[2])==0 then credit=math.min(maximum,credit+1) end
if credit>=ratio then return 1 end;return 0
`
