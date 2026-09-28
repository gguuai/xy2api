package admin

import (
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/liulixin-lex/xy2api/internal/pkg/response"
	"github.com/liulixin-lex/xy2api/internal/scheduling"
	"github.com/liulixin-lex/xy2api/internal/server/middleware"
)

func (h *SchedulingHandler) GetFailureDomains(c *gin.Context) {
	store, ok := h.store.(scheduling.FailureAdminStore)
	if !ok {
		schedulingError(c, scheduling.ErrSharedState)
		return
	}
	id, e := strconv.ParseInt(c.Param("id"), 10, 64)
	if e != nil || id <= 0 {
		response.BadRequest(c, "Invalid account ID")
		return
	}
	result, e := store.InspectFailureDomains(c.Request.Context(), id, strings.TrimSpace(c.Query("model")))
	if e != nil {
		schedulingError(c, e)
		return
	}
	response.Success(c, result)
}
func (h *SchedulingHandler) PutFailureDomains(c *gin.Context) {
	store, ok := h.store.(scheduling.FailureAdminStore)
	if !ok {
		schedulingError(c, scheduling.ErrSharedState)
		return
	}
	subject, ok := middleware.GetAuthSubjectFromContext(c)
	if !ok || subject.UserID <= 0 {
		response.Unauthorized(c, "Authenticated administrator required")
		return
	}
	id, e := strconv.ParseInt(c.Param("id"), 10, 64)
	if e != nil || id <= 0 {
		response.BadRequest(c, "Invalid account ID")
		return
	}
	var req struct {
		Version      *int64 `json:"expected_version"`
		Quota        string `json:"quota_pool_id"`
		Availability string `json:"availability_pool_id"`
	}
	if c.ShouldBindJSON(&req) != nil || req.Version == nil {
		response.BadRequest(c, "Explicit expected_version required")
		return
	}
	result, e := store.PutFailureDomains(c.Request.Context(), scheduling.AccountFailureDomains{AccountID: id, Version: *req.Version, QuotaPoolID: req.Quota, AvailabilityPoolID: req.Availability}, subject.UserID)
	if e != nil {
		schedulingError(c, e)
		return
	}
	response.Success(c, result)
}
func (h *SchedulingHandler) GetUnknownAttempt(c *gin.Context) {
	store, ok := h.store.(scheduling.FailureAdminStore)
	if !ok {
		schedulingError(c, scheduling.ErrSharedState)
		return
	}
	id := strings.TrimSpace(c.Param("ticket_id"))
	if id == "" || len(id) > 128 {
		response.BadRequest(c, "Invalid ticket ID")
		return
	}
	result, e := store.GetUnknownAttempt(c.Request.Context(), id)
	if e != nil {
		schedulingError(c, e)
		return
	}
	response.Success(c, result)
}
func (h *SchedulingHandler) ResolveUnknownAttempt(c *gin.Context) {
	store, ok := h.store.(scheduling.FailureAdminStore)
	if !ok {
		schedulingError(c, scheduling.ErrSharedState)
		return
	}
	subject, ok := middleware.GetAuthSubjectFromContext(c)
	if !ok || subject.UserID <= 0 {
		response.Unauthorized(c, "Authenticated administrator required")
		return
	}
	var req struct {
		Version *int64 `json:"expected_version"`
		scheduling.UnknownResolution
	}
	if c.ShouldBindJSON(&req) != nil || req.Version == nil {
		response.BadRequest(c, "Explicit expected_version and verified terminal evidence required")
		return
	}
	req.ExpectedVersion = *req.Version
	result, e := store.ResolveUnknownWithEvidence(c.Request.Context(), c.Param("ticket_id"), req.UnknownResolution, subject.UserID)
	if e != nil {
		schedulingError(c, e)
		return
	}
	response.Success(c, result)
}

func (h *SchedulingHandler) PermitFailureRecovery(c *gin.Context) {
	store, ok := h.store.(scheduling.FailureAdminStore)
	if !ok {
		schedulingError(c, scheduling.ErrSharedState)
		return
	}
	subject, ok := middleware.GetAuthSubjectFromContext(c)
	if !ok || subject.UserID <= 0 {
		response.Unauthorized(c, "Authenticated administrator required")
		return
	}
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || id <= 0 {
		response.BadRequest(c, "Invalid account ID")
		return
	}
	var req struct {
		Version       *int64 `json:"expected_version"`
		DomainVersion *int64 `json:"domain_version"`
		scheduling.FailureRecoveryEvidence
	}
	if c.ShouldBindJSON(&req) != nil || req.Version == nil || req.DomainVersion == nil {
		response.BadRequest(c, "Explicit versions and verified recovery evidence required")
		return
	}
	req.ExpectedVersion = *req.Version
	req.FailureRecoveryEvidence.DomainVersion = *req.DomainVersion
	result, err := store.PermitFailureRecovery(c.Request.Context(), id, req.FailureRecoveryEvidence, subject.UserID)
	if err == scheduling.ErrFailureDomainBlocked {
		response.Error(c, 409, "Recovery is occupied by an active or unknown attempt")
		return
	}
	if err != nil {
		schedulingError(c, err)
		return
	}
	response.Success(c, result)
}
