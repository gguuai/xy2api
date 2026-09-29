package admin

import (
	"context"
	"encoding/json"
	"github.com/gin-gonic/gin"
	"github.com/liulixin-lex/xy2api/internal/scheduling"
	"github.com/stretchr/testify/require"
	"net/http"
	"testing"
)

type modeAdminStub struct {
	schedulingAdminStub
	mode   scheduling.ModeSnapshot
	writes int
}

func (s *modeAdminStub) GetSchedulingMode(context.Context) (scheduling.ModeSnapshot, error) {
	return s.mode, s.err
}
func (s *modeAdminStub) PutSchedulingMode(_ context.Context, mode scheduling.Mode, v int64) (scheduling.ModeSnapshot, error) {
	s.writes++
	if s.err != nil {
		return scheduling.ModeSnapshot{}, s.err
	}
	if s.mode.Version != v {
		return scheduling.ModeSnapshot{}, scheduling.ErrVersionConflict
	}
	s.mode = scheduling.ModeSnapshot{Mode: mode, Version: v + 1}
	return s.mode, nil
}
func modeRouter(s *modeAdminStub) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	h := NewSchedulingHandler(s, nil)
	r.GET("/mode", h.GetSchedulingMode)
	r.PUT("/mode", h.PutSchedulingMode)
	return r
}
func modeBody(mode string, version int64) string {
	raw, _ := json.Marshal(map[string]any{"mode": mode, "expected_version": version})
	return string(raw)
}
func TestSchedulingModeHandlerContract(t *testing.T) {
	s := &modeAdminStub{mode: scheduling.ModeSnapshot{Mode: scheduling.ModeControlled}}
	r := modeRouter(s)
	w := schedulingTestCall(r, http.MethodGet, "/mode", "")
	require.Equal(t, 200, w.Code)
	w = schedulingTestCall(r, http.MethodPut, "/mode", modeBody("sub2api", 0))
	require.Equal(t, 200, w.Code)
	require.Equal(t, scheduling.ModeSub2API, s.mode.Mode)
	w = schedulingTestCall(r, http.MethodPut, "/mode", modeBody("controlled", 0))
	require.Equal(t, 409, w.Code)
	require.Equal(t, scheduling.ModeSub2API, s.mode.Mode)
	for _, body := range []string{"{}", modeBody("legacy", 1), modeBody("controlled", -1), modeBody("controlled", 1) + "{}", "{\"mode\":\"controlled\"}", "{\"mode\":\"controlled\",\"expected_version\":1,\"extra\":true}"} {
		before := s.writes
		w = schedulingTestCall(r, http.MethodPut, "/mode", body)
		require.Equal(t, 400, w.Code, body)
		require.Equal(t, before, s.writes)
	}
	s.err = scheduling.ErrSharedState
	w = schedulingTestCall(r, http.MethodGet, "/mode", "")
	require.Equal(t, 503, w.Code)
}
