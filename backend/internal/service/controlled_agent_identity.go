package service

import (
	"context"
	"net/http"
	"time"

	"github.com/liulixin-lex/xy2api/internal/scheduling"
)

// Keep credential repair available without silently replaying an inference on
// the same account. The repaired task is usable by a later logical request.
func (s *OpenAIGatewayService) repairControlledAgentIdentityTask(ctx context.Context, account *Account, status int, body []byte) error {
	if !ControlledSchedulingEnabled(ctx) || !isAgentIdentityTaskInvalidHTTPResponse(status, body) || !s.isAgentIdentityAccount(ctx, account) {
		return nil
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	repairCtx := ctx
	if r := controlledRequest(ctx); r != nil {
		r.mu.Lock()
		ledger := r.Ledger
		r.mu.Unlock()
		if ledger != nil {
			deadline := ledger.Snapshot().Deadline
			if !deadline.IsZero() {
				if !time.Now().Before(deadline) {
					return scheduling.ErrDeadline
				}
				var cancel context.CancelFunc
				repairCtx, cancel = context.WithDeadline(ctx, deadline)
				defer cancel()
			}
		}
	}
	reason := GatewayFailureReason("agent_task_repaired")
	if err := s.recoverAgentIdentityTask(repairCtx, account, account.GetCredential("task_id")); err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		reason = "agent_task_repair_failed"
	}
	return &UpstreamFailoverError{
		StatusCode:   http.StatusBadGateway,
		ResponseBody: []byte("{\"error\":{\"type\":\"upstream_error\",\"code\":\"agent_task_unavailable\",\"message\":\"Upstream account task is unavailable\"}}"),
		Stage:        GatewayFailureStageAccountAuth, Scope: GatewayFailureScopeAccount,
		Reason: reason, NextAccountAction: NextAccountRetry,
	}
}
