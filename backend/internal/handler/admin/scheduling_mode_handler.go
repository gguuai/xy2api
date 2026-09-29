package admin

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/liulixin-lex/xy2api/internal/pkg/response"
	"github.com/liulixin-lex/xy2api/internal/scheduling"
	"github.com/liulixin-lex/xy2api/internal/server/middleware"
)

func (h *SchedulingHandler) SchedulingModeSnapshot(ctx context.Context) (scheduling.ModeSnapshot, error) {
	if h == nil {
		return scheduling.ModeSnapshot{}, scheduling.ErrSharedState
	}
	store, ok := h.store.(scheduling.ModeStore)
	if !ok {
		return scheduling.ModeSnapshot{}, scheduling.ErrSharedState
	}
	return store.GetSchedulingMode(ctx)
}
func (h *SchedulingHandler) GetSchedulingMode(c *gin.Context) {
	mode, err := h.SchedulingModeSnapshot(c.Request.Context())
	if err != nil {
		schedulingError(c, err)
		return
	}
	response.Success(c, mode)
}
func (h *SchedulingHandler) PutSchedulingMode(c *gin.Context) {
	var input struct {
		Mode            scheduling.Mode `json:"mode"`
		ExpectedVersion *int64          `json:"expected_version"`
	}
	decoder := json.NewDecoder(http.MaxBytesReader(c.Writer, c.Request.Body, 4096))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&input); err != nil || !input.Mode.Valid() || input.ExpectedVersion == nil || *input.ExpectedVersion < 0 {
		response.BadRequest(c, "Valid mode and expected_version are required")
		return
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		response.BadRequest(c, "Only one mode object is allowed")
		return
	}
	store, ok := h.store.(scheduling.ModeStore)
	if !ok {
		schedulingError(c, scheduling.ErrSharedState)
		return
	}
	middleware.SetAuditAction(c, "scheduling.mode.update")
	middleware.SetAuditExtra(c, map[string]any{"mode": input.Mode, "config_version": *input.ExpectedVersion})
	result, err := store.PutSchedulingMode(c.Request.Context(), input.Mode, *input.ExpectedVersion)
	if err != nil {
		schedulingError(c, err)
		return
	}
	response.Success(c, result)
}
