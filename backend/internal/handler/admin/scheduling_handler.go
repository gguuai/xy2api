package admin

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/liulixin-lex/xy2api/internal/pkg/response"
	"github.com/liulixin-lex/xy2api/internal/scheduling"
)

// SchedulingExplainFunc must be read-only: no selection, quota, probe or upstream call.
type SchedulingExplainFunc func(context.Context, json.RawMessage) (any, error)
type SchedulingHandler struct {
	store   scheduling.SchedulingAdminStore
	explain SchedulingExplainFunc
}

func NewSchedulingHandler(store scheduling.SchedulingAdminStore, explain SchedulingExplainFunc) *SchedulingHandler {
	return &SchedulingHandler{store: store, explain: explain}
}
func (h *SchedulingHandler) SetExplain(explain SchedulingExplainFunc) { h.explain = explain }
func schedulingError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, scheduling.ErrVersionConflict):
		response.Error(c, http.StatusConflict, "Scheduling configuration changed; reload before saving")
	case errors.Is(err, scheduling.ErrControlNotFound):
		response.Error(c, http.StatusNotFound, "Scheduling account not found")
	case errors.Is(err, scheduling.ErrInvalidControl):
		response.Error(c, http.StatusBadRequest, err.Error())
	case errors.Is(err, scheduling.ErrControlBlocked):
		response.Error(c, http.StatusConflict, err.Error())
	default:
		response.Error(c, http.StatusServiceUnavailable, "Scheduling authoritative state unavailable")
	}
}
func (h *SchedulingHandler) GetPolicy(c *gin.Context) {
	if h.store == nil {
		schedulingError(c, scheduling.ErrSharedState)
		return
	}
	groupID, err := strconv.ParseInt(c.DefaultQuery("group_id", "0"), 10, 64)
	model := strings.TrimSpace(c.Query("model"))
	if err != nil || groupID < 0 || model == "" {
		response.BadRequest(c, "group_id and model are required")
		return
	}
	rec, err := h.store.GetPolicy(c.Request.Context(), groupID, model)
	if err != nil {
		schedulingError(c, err)
		return
	}
	response.Success(c, rec)
}
func (h *SchedulingHandler) PutPolicy(c *gin.Context) {
	if h.store == nil {
		schedulingError(c, scheduling.ErrSharedState)
		return
	}
	var req struct {
		GroupID         int64             `json:"group_id"`
		Model           string            `json:"model"`
		ExpectedVersion *int64            `json:"expected_version"`
		Policy          scheduling.Policy `json:"policy"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || req.ExpectedVersion == nil || *req.ExpectedVersion < 0 || req.Model == "" || req.GroupID < 0 {
		response.BadRequest(c, "Valid group_id, model, expected_version and policy are required")
		return
	}
	req.Policy.GroupID = req.GroupID
	req.Policy.Model = req.Model
	if err := scheduling.ValidatePolicy(req.Policy); err != nil {
		response.BadRequest(c, err.Error())
		return
	}
	rec, err := h.store.PutPolicy(c.Request.Context(), req.Policy, *req.ExpectedVersion)
	if err != nil {
		schedulingError(c, err)
		return
	}
	response.Success(c, rec)
}
func (h *SchedulingHandler) Explain(c *gin.Context) {
	if h.explain == nil {
		response.Error(c, http.StatusServiceUnavailable, "Scheduling explanation unavailable")
		return
	}
	var body json.RawMessage
	if err := c.ShouldBindJSON(&body); err != nil {
		response.BadRequest(c, "Invalid explanation input")
		return
	}
	result, err := h.explain(c.Request.Context(), body)
	if err != nil {
		schedulingError(c, err)
		return
	}
	response.Success(c, result)
}
func (h *SchedulingHandler) GetControl(c *gin.Context) {
	if h.store == nil {
		schedulingError(c, scheduling.ErrSharedState)
		return
	}
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || id <= 0 {
		response.BadRequest(c, "Invalid account ID")
		return
	}
	result, err := h.store.GetControl(c.Request.Context(), id, c.DefaultQuery("scope", scheduling.ScopeAccount))
	if err != nil {
		schedulingError(c, err)
		return
	}
	response.Success(c, result)
}
func (h *SchedulingHandler) Control(c *gin.Context) {
	if h.store == nil {
		schedulingError(c, scheduling.ErrSharedState)
		return
	}
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || id <= 0 {
		response.BadRequest(c, "Invalid account ID")
		return
	}
	var cmd scheduling.ControlCommand
	if err = c.ShouldBindJSON(&cmd); err != nil || cmd.ExpectedEpoch == nil || *cmd.ExpectedEpoch < 0 {
		response.BadRequest(c, "Valid action, scope and expected_epoch are required")
		return
	}
	cmd.AccountID = id
	result, err := h.store.Control(c.Request.Context(), cmd)
	if err != nil {
		schedulingError(c, err)
		return
	}
	response.Success(c, result)
}
func (h *AccountHandler) SetSchedulingController(store scheduling.SchedulingAdminStore) {
	h.schedulingController = store
}

func (h *SchedulingHandler) ListRequestAttempts(c *gin.Context) {
	requestID := strings.TrimSpace(c.Param("request_id"))
	if requestID == "" || len(requestID) > 256 {
		response.BadRequest(c, "Invalid scheduling request ID")
		return
	}
	reader, ok := h.store.(scheduling.SchedulingAttemptReader)
	if !ok {
		schedulingError(c, scheduling.ErrSharedState)
		return
	}
	attempts, err := reader.ListRequestAttempts(c.Request.Context(), requestID)
	if err != nil {
		schedulingError(c, err)
		return
	}
	response.Success(c, gin.H{"request_id": requestID, "attempts": attempts})
}

func (h *SchedulingHandler) GetStatistics(c *gin.Context) {
	groupID, err := strconv.ParseInt(c.DefaultQuery("group_id", "0"), 10, 64)
	model := strings.TrimSpace(c.Query("model"))
	until := time.Now().UTC()
	since := until.Add(-24 * time.Hour)
	if err != nil || groupID < 0 || model == "" {
		response.BadRequest(c, "Valid group_id and model are required")
		return
	}
	if raw := c.Query("since"); raw != "" {
		since, err = time.Parse(time.RFC3339Nano, raw)
		if err != nil || !since.Before(until) {
			response.BadRequest(c, "since must be an RFC3339 time in the past")
			return
		}
	}
	reader, ok := h.store.(scheduling.SchedulingStatsReader)
	if !ok {
		schedulingError(c, scheduling.ErrSharedState)
		return
	}
	result, err := reader.DispatchStatistics(c.Request.Context(), groupID, model, since, until)
	if err != nil {
		schedulingError(c, err)
		return
	}
	response.Success(c, result)
}
