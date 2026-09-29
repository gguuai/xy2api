package scheduling

import (
	"context"
	"time"
)

// SelectAuxiliary preserves group priority and weights for non-generation
// endpoints. Its independent SWRR stream cannot distort generation shares,
// consume a recovery probe, or leave a generation reservation behind.
func (r *Runtime) SelectAuxiliary(ctx context.Context, req SelectionRequest) (Decision, error) {
	if r == nil || r.store == nil || r.store.client == nil {
		return Decision{}, ErrSharedState
	}
	req = accountPoolLocalRequest(req)
	states, err := r.store.Snapshots(ctx, accountPoolHealthRequest(req))
	if err != nil {
		return Decision{}, err
	}
	for id, state := range states {
		state = freshAccountPoolHealth(state, req.Now)
		if state.State == HealthHalfOpen {
			state.State = HealthOpen
			state.CooldownUntilMS = req.Now.Add(time.Minute).UnixMilli()
		}
		states[id] = state
	}
	options, err := evaluateAccountPool(req, states)
	if err != nil {
		return Decision{}, err
	}
	pool := accountPoolAllocationKey(req, options) + ":auxiliary"
	weights := make([]allocationWeight, 0, len(options))
	for _, d := range options {
		weights = append(weights, allocationWeight{d.AccountID, d.EffectiveShare})
	}
	id, receipt, err := r.store.reserve(ctx, pool, weights, req.Now)
	if err != nil {
		return Decision{}, err
	}
	for _, d := range options {
		if d.AccountID != id {
			continue
		}
		d.ReservationID, d.PoolKey = receipt, pool
		if err = r.store.finalize(ctx, d, false); err != nil {
			return Decision{}, err
		}
		d.ReservationID = ""
		d.Reason = "auxiliary_account_priority"
		return d, nil
	}
	return Decision{}, ErrSharedState
}
