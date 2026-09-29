package scheduling

import (
	"context"
	"errors"
	"log/slog"
	"time"
)

// ReadmitAccountPoolHealth performs pre-dispatch admission for an adapter's
// actual upstream model/identity, or a verified protocol owner that did not go
// through weighted selection. actual supplies only observation identity fields;
// it is not a terminal observation. boundAccountID must come from a validated
// protocol binding, never a reason string, and is zero for ordinary selection.
//
// Success preserves the original SWRR receipt and transfers ownership of the
// returned probe token to the caller. Every failure compensates the old receipt
// and all probe tokens it owns; callers must clear their pending decision. PG
// failure/control/capacity and the real-attempt ledger remain mandatory after
// this operation. No callback here dispatches or spends a physical attempt.
func (r *Runtime) ReadmitAccountPoolHealth(ctx context.Context, selected Decision, actual Observation, boundAccountID int64) (Decision, error) {
	if r == nil || r.store == nil || r.store.client == nil {
		return Decision{}, ErrSharedState
	}
	newProbe := ""
	fail := func(cause error) (Decision, error) {
		// Compensation respects the remaining request budget. Only an expired
		// cleanup gets one detached retry; it never delays the response past D.
		cleanupErr := compensateAccountPoolReadmission(ctx, func(cleanup context.Context) error {
			probeErr := r.store.ReleaseProbe(cleanup, newProbe)
			selectionErr := r.ReleaseSelection(cleanup, selected)
			return errors.Join(probeErr, selectionErr)
		})
		return Decision{}, errors.Join(cause, cleanupErr)
	}
	if selected.AccountID <= 0 || selected.AccountID != actual.AccountID || actual.Model == "" || actual.Profile.Name != AccountPoolProfileName || actual.Profile.HealthRevision < 0 || boundAccountID < 0 || (boundAccountID > 0 && boundAccountID != selected.AccountID) {
		return fail(ErrHealthIdentity)
	}
	if boundAccountID == 0 && (selected.HealthFence == nil || selected.ReservationID == "" || selected.PoolKey == "") {
		return fail(ErrHealthSelectionStale)
	}
	if err := ctx.Err(); err != nil {
		return fail(err)
	}

	// Rechecking the same identity must not refresh away a stale generation or
	// stage. Strict Freeze also validates an existing recovery lease.
	if old := selected.HealthFence; old != nil && old.Model == actual.Model && old.HealthIdentity == actual.HealthIdentity {
		fence, err := r.FreezeSelectedHealth(ctx, actual.AccountID, actual.Model, actual.Profile, actual.Reasoning, actual.ContextBucket, actual.Transport, actual.HealthIdentity, selected)
		if err != nil {
			return fail(err)
		}
		selected.HealthFence = &fence
		return selected, nil
	}

	req := SelectionRequest{
		Policy:  Policy{AccountPool: true, Model: actual.Model},
		Profile: actual.Profile, Reasoning: actual.Reasoning,
		ContextBucket: actual.ContextBucket, Transport: actual.Transport, Now: time.Now(),
		Candidates: []Candidate{{AccountID: actual.AccountID, HealthModel: actual.Model, HealthIdentity: actual.HealthIdentity, HardEligible: true, CapacityAvailable: true}},
	}
	states, err := r.store.Snapshots(ctx, req)
	if err != nil {
		return fail(err)
	}
	health := freshAccountPoolHealth(states[actual.AccountID], req.Now)
	if health.State == HealthOpen {
		return fail(ErrHealthSelectionStale)
	}
	readmitted := selected
	readmitted.HealthState = health.State
	readmitted.HealthFence = &HealthFence{Model: actual.Model, HealthIdentity: actual.HealthIdentity, Generation: health.Generation, StageRevision: health.StageRevision, HealthRevision: actual.Profile.HealthRevision, State: health.State}
	readmitted.Probe = health.State == HealthHalfOpen
	readmitted.ProbeToken = ""
	if readmitted.Probe {
		newProbe, err = r.store.acquireProbe(ctx, req, actual.AccountID, false)
		if err != nil {
			return fail(err)
		}
		if newProbe == "" {
			return fail(ErrCapacity)
		}
		readmitted.ProbeToken = newProbe
	}
	// The lease and actual generation/stage are checked together under WATCH;
	// an OPEN, eviction, or lost lease between reading and freezing is rejected.
	fence, err := r.FreezeSelectedHealth(ctx, actual.AccountID, actual.Model, actual.Profile, actual.Reasoning, actual.ContextBucket, actual.Transport, actual.HealthIdentity, readmitted)
	if err != nil {
		return fail(err)
	}
	readmitted.HealthFence = &fence
	if selected.ProbeToken != "" && selected.ProbeToken != newProbe {
		if err = r.store.ReleaseProbe(ctx, selected.ProbeToken); err != nil {
			return fail(err)
		}
	}
	return readmitted, nil
}

func compensateAccountPoolReadmission(ctx context.Context, operation func(context.Context) error) error {
	bounded, stop := context.WithTimeout(ctx, 5*time.Second)
	observed := bounded.Err()
	if observed == nil {
		observed = operation(bounded)
		expired := bounded.Err() != nil || errors.Is(observed, context.Canceled) || errors.Is(observed, context.DeadlineExceeded)
		stop()
		if observed == nil || !expired {
			return observed
		}
	} else {
		stop()
	}
	go func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := operation(cleanup); err != nil {
			slog.Warn("account pool readmission compensation failed", "error", err)
		}
	}()
	return observed
}
