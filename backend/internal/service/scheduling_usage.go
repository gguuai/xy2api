package service

import (
	"context"
	"errors"
	"time"

	"github.com/liulixin-lex/xy2api/internal/scheduling"
)

type schedulingUsageContextKey struct{}
type schedulingUsageBinding struct {
	AttemptID string
	AccountID int64
}

// SchedulingAttemptIDFromContext is safe only after the turn's dispatch finishes.
func SchedulingAttemptIDFromContext(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	if b, ok := ctx.Value(schedulingUsageContextKey{}).(schedulingUsageBinding); ok {
		return b.AttemptID
	}
	r := controlledRequest(ctx)
	if r == nil {
		return ""
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.currentAttemptID != "" {
		return r.currentAttemptID
	}
	if len(r.history) == 0 {
		return ""
	}
	return r.history[len(r.history)-1].AttemptID
}

// Freeze before enqueue: subsequent WS turns and retries must not change a bill's identity.
func CaptureSchedulingUsageContext(ctx context.Context) context.Context {
	if ctx == nil {
		return context.Background()
	}
	if _, ok := ctx.Value(schedulingUsageContextKey{}).(schedulingUsageBinding); ok {
		return ctx
	}
	r := controlledRequest(ctx)
	if r == nil {
		return ctx
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.currentAttemptID != "" {
		return context.WithValue(ctx, schedulingUsageContextKey{}, schedulingUsageBinding{AttemptID: r.currentAttemptID, AccountID: r.Decision.AccountID})
	}
	if len(r.history) == 0 {
		return ctx
	}
	last := r.history[len(r.history)-1]
	return context.WithValue(ctx, schedulingUsageContextKey{}, schedulingUsageBinding{AttemptID: last.AttemptID, AccountID: last.AccountID})
}
func WithSchedulingUsageAttempt(ctx context.Context, ticketID string) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	if ticketID == "" {
		return ctx
	}
	return context.WithValue(ctx, schedulingUsageContextKey{}, schedulingUsageBinding{AttemptID: ticketID})
}
func CopySchedulingUsageContext(dst, src context.Context) context.Context {
	if dst == nil {
		dst = context.Background()
	}
	if src != nil {
		if b, ok := src.Value(schedulingUsageContextKey{}).(schedulingUsageBinding); ok {
			return context.WithValue(dst, schedulingUsageContextKey{}, b)
		}
	}
	return dst
}
func schedulingUsageBound(ctx context.Context) bool {
	if ctx == nil {
		return false
	}
	b, ok := ctx.Value(schedulingUsageContextKey{}).(schedulingUsageBinding)
	return ok && b.AttemptID != ""
}
func acknowledgeSchedulingUsage(ctx context.Context, control *ControlledSchedulingService, accountID int64) error {
	if control == nil || ctx == nil {
		return nil
	}
	b, ok := ctx.Value(schedulingUsageContextKey{}).(schedulingUsageBinding)
	if !ok || b.AttemptID == "" {
		return nil
	}
	detached, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	ticket, _, err := control.Store.Attempt(detached, b.AttemptID)
	if err != nil {
		return err
	}
	if ticket.AccountID != accountID {
		return scheduling.ErrAttemptIdentity
	}
	return control.Store.AcknowledgeUsage(detached, b.AttemptID)
}

// A controlled attempt acknowledges accounting only after durable log storage.
// Legacy requests keep their existing batching behavior.
func writeSchedulingUsageLog(ctx context.Context, repo UsageLogRepository, usageLog *UsageLog, logKey string) error {
	if !schedulingUsageBound(ctx) {
		writeUsageLogBestEffort(ctx, repo, usageLog, logKey)
		return nil
	}
	if repo == nil || usageLog == nil {
		return errors.New("scheduling usage log persistence unavailable")
	}
	detached, cancel := detachedBillingContext(ctx)
	defer cancel()
	_, err := repo.Create(detached, usageLog)
	return err
}
