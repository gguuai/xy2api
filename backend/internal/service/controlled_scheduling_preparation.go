package service

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/liulixin-lex/xy2api/internal/scheduling"
)

// A preparation context bounds control-plane I/O by the request's remaining D.
// It must never become the upstream response body's parent: D ends at the first
// semantic output, while a successful long response can continue beyond it.
func controlledPreparationContext(ctx context.Context, r *ControlledRequest) (context.Context, context.CancelFunc) {
	if r == nil {
		return ctx, func() {}
	}
	r.mu.Lock()
	enabled, ledger, hasOutput := r.Policy.Enabled, r.Ledger, !r.semanticAt.IsZero()
	r.mu.Unlock()
	if !enabled || ledger == nil || hasOutput {
		return ctx, func() {}
	}
	deadline := ledger.Snapshot().Deadline
	if deadline.IsZero() {
		return ctx, func() {}
	}
	return context.WithDeadline(ctx, deadline)
}

func controlledPreparationError(parent, bounded context.Context, err error) error {
	if err != nil && parent.Err() == nil && errors.Is(bounded.Err(), context.DeadlineExceeded) {
		return scheduling.ErrDeadline
	}
	return err
}

// A changed admission snapshot is not an upstream attempt. The existing request
// budget and a finite invalidation cap prevent selection-only retry loops.
func (s *ControlledSchedulingService) invalidateControlledSelection(ctx context.Context, r *ControlledRequest, decision scheduling.Decision) error {
	s.releaseDecisionContext(ctx, decision)
	r.mu.Lock()
	r.decisionPending = false
	r.gateRejections++
	rejected := r.gateRejections
	r.mu.Unlock()
	if rejected >= 128 {
		return scheduling.ErrAttemptBudget
	}
	return &UpstreamFailoverError{StatusCode: 503, PreDispatchSelectionInvalidated: true, ClientMessage: "candidate lost dispatch admission"}
}

// Preparation cleanup observes the same D as the operation it compensates.
// A timed-out cleanup gets one detached, bounded retry of the existing
// compare-token/receipt-idempotent operation. There is no resident worker or
// polling loop; the returned channel closes when this finite cleanup ends.
func controlledCompensate(ctx context.Context, action string, operation func(context.Context) error) <-chan struct{} {
	done := make(chan struct{})
	bounded, cancel := context.WithTimeout(ctx, 5*time.Second)
	if bounded.Err() == nil {
		err := operation(bounded)
		expired := bounded.Err() != nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)
		cancel()
		if err == nil {
			close(done)
			return done
		}
		if !expired {
			slog.Warn(action, "error", err)
			close(done)
			return done
		}
	} else {
		cancel()
	}
	go func() {
		defer close(done)
		cleanup, stop := context.WithTimeout(context.Background(), 5*time.Second)
		defer stop()
		if err := operation(cleanup); err != nil {
			slog.Warn(action, "error", err)
		}
	}()
	return done
}

func (s *ControlledSchedulingService) releaseDecisionContext(ctx context.Context, decision scheduling.Decision) {
	if s == nil || s.Runtime == nil || (decision.ReservationID == "" && decision.ProbeToken == "") {
		return
	}
	controlledCompensate(ctx, "scheduling selection compensation pending", func(cleanup context.Context) error { return s.Runtime.ReleaseSelection(cleanup, decision) })
}
func (s *ControlledSchedulingService) refundDispatchBudgetContext(ctx context.Context, reservation scheduling.BudgetReservation) {
	if s == nil || s.Runtime == nil || reservation.ID == "" {
		return
	}
	controlledCompensate(ctx, "scheduling budget compensation pending", func(cleanup context.Context) error { return s.Runtime.RefundDispatchBudget(cleanup, reservation) })
}

// A preparation timeout has proven no upstream send. Return at D while the
// existing ticket remains capacity-bearing until one finite terminal settlement.
// Detach the request cleanup hook first so Close cannot wait on the same Once.
func (d *controlledDispatch) finishPreparationFailure(err error) {
	if d == nil {
		return
	}
	d.mu.Lock()
	sent := d.sent
	d.mu.Unlock()
	asynchronous := false
	if !sent {
		prepare, stop := controlledPreparationContext(d.ctx, d.request)
		asynchronous = prepare.Err() != nil || d.ctx.Err() != nil || errors.Is(err, scheduling.ErrDeadline) || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)
		stop()
	}
	if !asynchronous {
		d.Finish("not_sent", true, err)
		return
	}
	d.request.mu.Lock()
	d.request.finish = nil
	d.request.decisionPending = false
	d.request.mu.Unlock()
	d.cancel()
	go d.Finish("not_sent", true, err)
}
