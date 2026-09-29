package admin

import (
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"net/http"
	"testing"
)

func TestOneClickSchedulingRetiredControlsCannotWrite(t *testing.T) {
	gin.SetMode(gin.TestMode)
	store := &schedulingAdminStub{}
	h := NewSchedulingHandler(store, nil)
	router := gin.New()
	router.POST("/accounts/:id/scheduling-control", h.RetiredAccountScheduling)
	require.Equal(t, http.StatusGone, schedulingTestCall(router, http.MethodPost, "/accounts/1/scheduling-control", "{\"action\":\"force_stop\"}").Code)
	require.Zero(t, store.writes)
	require.Zero(t, store.reads)
}
