package handler

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/liulixin-lex/xy2api/internal/scheduling"
	"github.com/stretchr/testify/require"
)

func TestControlledSchedulingStopResponse(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, tc := range []struct {
		err    error
		status int
		code   string
	}{{scheduling.ErrAttemptBudget, 503, "scheduling_attempt_budget_exhausted"}, {scheduling.ErrDeadline, 504, "scheduling_deadline_exhausted"}, {scheduling.ErrCommitted, 409, "scheduling_response_committed"}, {scheduling.ErrRetryBudget, 503, "scheduling_retry_budget_exhausted"}} {
		t.Run(tc.code, func(t *testing.T) {
			rec := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(rec)
			c.Request = httptest.NewRequest("POST", "/v1/responses", nil)
			require.True(t, handleControlledSchedulingStop(c, fmt.Errorf("transport: %w", tc.err)))
			require.Equal(t, tc.status, rec.Code)
			require.Contains(t, rec.Body.String(), tc.code)
		})
	}
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest("POST", "/v1/responses", nil)
	c.Writer.WriteHeader(http.StatusOK)
	_, _ = c.Writer.WriteString("data: existing output\n\n")
	require.True(t, handleControlledSchedulingStop(c, scheduling.ErrCommitted))
	require.Equal(t, "data: existing output\n\n", rec.Body.String())
	require.False(t, handleControlledSchedulingStop(c, errors.New("ordinary upstream error")))
}
