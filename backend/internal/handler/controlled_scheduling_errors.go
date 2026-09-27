package handler

import (
	"errors"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/liulixin-lex/xy2api/internal/scheduling"
	"github.com/liulixin-lex/xy2api/internal/service"
)

// Local admission failures never enter the upstream retry/health machinery.
func handleControlledSchedulingStop(c *gin.Context, err error) bool {
	if !service.IsControlledSchedulingStop(err) {
		return false
	}
	status, code, message := http.StatusServiceUnavailable, "scheduling_unavailable", "Shared scheduling state is unavailable"
	switch {
	case errors.Is(err, scheduling.ErrCommitted):
		status, code, message = http.StatusConflict, "scheduling_response_committed", "The response has already started and cannot be replayed"
	case errors.Is(err, scheduling.ErrDeadline):
		status, code, message = http.StatusGatewayTimeout, "scheduling_deadline_exhausted", "The request first-output time budget is exhausted"
	case errors.Is(err, scheduling.ErrAttemptBudget):
		code, message = "scheduling_attempt_budget_exhausted", "The request attempt budget is exhausted"
	case errors.Is(err, scheduling.ErrRetryBudget):
		code, message = "scheduling_retry_budget_exhausted", "The shared retry budget is exhausted"
	case errors.Is(err, scheduling.ErrUnsafeReplay):
		status, code, message = http.StatusConflict, "scheduling_replay_unsafe", "This request cannot be safely replayed"
	case errors.Is(err, scheduling.ErrControlBlocked):
		code, message = "scheduling_account_paused", "The required account is paused or draining"
	case errors.Is(err, scheduling.ErrCapacity):
		code, message = "scheduling_capacity_exhausted", "Eligible accounts have no available capacity"
	}
	c.Set("scheduling_stop_reason", code)
	if c.Request != nil {
		defer service.RecordControlledSchedulingStop(c.Request.Context(), code)
	}
	if !c.Writer.Written() {
		if c.Request != nil && strings.Contains(c.Request.URL.Path, "/v1beta/") {
			c.AbortWithStatusJSON(status, gin.H{"error": gin.H{"code": status, "status": "UNAVAILABLE", "message": message, "reason": code}})
		} else {
			c.AbortWithStatusJSON(status, gin.H{"type": "error", "error": gin.H{"type": code, "code": code, "message": message}})
		}
	} else {
		c.Abort()
	}
	return true
}

// Selection retries and upstream attempts have separate bounds. Admission races
// cannot consume the actual-attempt budget; the shared ledger owns that budget.
func controlledAccountSwitchExhausted(c *gin.Context, legacyCount, legacyLimit int) bool {
	if c.Request == nil || !service.ControlledSchedulingEnabled(c.Request.Context()) {
		return legacyCount >= legacyLimit
	}
	if c.Request.Context().Err() != nil {
		return true
	}
	const key = "controlled_selection_iterations"
	n := c.GetInt(key) + 1
	c.Set(key, n)
	return n >= 128
}
