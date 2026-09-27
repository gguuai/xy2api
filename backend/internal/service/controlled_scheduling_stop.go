package service

import (
	"context"
	"log/slog"
	"time"
)

// RecordControlledSchedulingStop preserves the logical request's final stop
// reason separately from the last upstream attempt's outcome. Selection failure
// does not create a fictitious dispatch ticket or consume an attempt.
func RecordControlledSchedulingStop(ctx context.Context, reason string) {
	if ctx == nil {
		return
	}
	r := controlledRequest(ctx)
	if r == nil || reason == "" {
		return
	}
	r.mu.Lock()
	id, ticket, control := r.ID, r.currentAttemptID, r.control
	for i := range r.history {
		if r.history[i].AttemptID == ticket {
			r.history[i].StopReason = reason
		}
	}
	r.mu.Unlock()
	slog.Info("scheduling_request_stopped", "request_id", id, "attempt_id", ticket, "stop_reason", reason)
	if ticket == "" || control == nil || control.Store == nil {
		return
	}
	recordCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
	defer cancel()
	if err := control.Store.RecordAttemptMetrics(recordCtx, ticket, map[string]any{"stop_reason": reason}); err != nil {
		slog.Warn("scheduling stop reason persistence pending", "request_id", id, "attempt_id", ticket, "error", err)
	}
}
