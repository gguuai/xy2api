package admin

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"
	"github.com/liulixin-lex/xy2api/internal/pkg/response"
	"github.com/liulixin-lex/xy2api/internal/scheduling"
	"github.com/liulixin-lex/xy2api/internal/server/middleware"
)

func groupSchedulingError(c *gin.Context, err error) {
	if errors.Is(err, scheduling.ErrSchedulingGroupNotFound) {
		response.NotFound(c, "Scheduling group not found")
		return
	}
	schedulingError(c, err)
}

func (h *SchedulingHandler) GetGroupPolicy(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || id < 0 {
		response.BadRequest(c, "Invalid scheduling group ID")
		return
	}
	store, ok := h.store.(scheduling.SchedulingGroupPolicyStore)
	if !ok {
		groupSchedulingError(c, scheduling.ErrSharedState)
		return
	}
	rec, err := store.GetGroupPolicy(c.Request.Context(), id)
	if err != nil {
		groupSchedulingError(c, err)
		return
	}
	response.Success(c, rec)
}

// Pointers distinguish explicit zero (highest priority or disabled weight) from
// omitted input. Full validation runs before the store can begin a transaction.
type groupPolicyInput struct {
	GroupID  *int64 `json:"group_id"`
	Version  int64  `json:"version"`
	Accounts []struct {
		AccountID int64  `json:"account_id"`
		Priority  *int   `json:"priority"`
		Weight    *int64 `json:"traffic_weight"`
		FillOrder int    `json:"fill_order"`
	} `json:"accounts"`
	FirstOutputTimeoutMS *int64 `json:"first_output_timeout_ms"`
	TotalWaitTimeoutMS   *int64 `json:"total_wait_timeout_ms"`
	MaxAttempts          *int   `json:"max_attempts"`
}

func (h *SchedulingHandler) PutGroupPolicy(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || id < 0 {
		response.BadRequest(c, "Invalid scheduling group ID")
		return
	}
	store, ok := h.store.(scheduling.SchedulingGroupPolicyStore)
	if !ok {
		groupSchedulingError(c, scheduling.ErrSharedState)
		return
	}
	var req struct {
		ExpectedVersion *int64            `json:"expected_version"`
		Policy          *groupPolicyInput `json:"policy"`
	}
	decoder := json.NewDecoder(http.MaxBytesReader(c.Writer, c.Request.Body, 4<<20))
	decoder.DisallowUnknownFields()
	if err = decoder.Decode(&req); err != nil {
		response.BadRequest(c, "A valid group account policy is required")
		return
	}
	if err = decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		response.BadRequest(c, "Only one policy object is allowed")
		return
	}
	if req.ExpectedVersion == nil || *req.ExpectedVersion < 0 || req.Policy == nil {
		response.BadRequest(c, "expected_version and policy are required")
		return
	}
	input := req.Policy
	if input.Accounts == nil || input.GroupID == nil || *input.GroupID != id || input.FirstOutputTimeoutMS == nil || input.TotalWaitTimeoutMS == nil || input.MaxAttempts == nil {
		response.BadRequest(c, "Policy group must match the URL and all waiting settings are required")
		return
	}
	p := scheduling.GroupPolicy{GroupID: id, Version: input.Version, Accounts: []scheduling.AccountRule{}, FirstOutputTimeoutMS: *input.FirstOutputTimeoutMS, TotalWaitTimeoutMS: *input.TotalWaitTimeoutMS, MaxAttempts: *input.MaxAttempts}
	for _, a := range input.Accounts {
		if a.Weight == nil {
			response.BadRequest(c, "Every account requires an explicit traffic_weight")
			return
		}
		p.Accounts = append(p.Accounts, scheduling.AccountRule{AccountID: a.AccountID, Priority: a.Priority, Weight: *a.Weight, FillOrder: a.FillOrder})
	}
	if err = scheduling.ValidateGroupPolicy(p); err != nil {
		response.BadRequest(c, err.Error())
		return
	}
	middleware.SetAuditAction(c, "scheduling.group_policy.update")
	middleware.SetAuditExtra(c, map[string]any{"group_id": id, "config_version": *req.ExpectedVersion, "requested_count": len(p.Accounts)})
	rec, err := store.PutGroupPolicy(c.Request.Context(), p, *req.ExpectedVersion)
	if err != nil {
		groupSchedulingError(c, err)
		return
	}
	response.Success(c, rec)
}

// Prevent successful writes to settings no longer read by the scheduler.
func (h *SchedulingHandler) RetiredModelPolicy(c *gin.Context) {
	response.Error(c, http.StatusGone, "Model-specific scheduling settings have been retired; use group account scheduling")
}
