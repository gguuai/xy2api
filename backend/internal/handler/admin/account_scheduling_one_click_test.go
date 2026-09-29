package admin

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/liulixin-lex/xy2api/internal/service"
	"github.com/stretchr/testify/require"
)

type oneClickAccountService struct {
	*stubAdminService
	enabled map[int64]bool
	writes  []int64
}

func (s *oneClickAccountService) GetAccount(_ context.Context, id int64) (*service.Account, error) {
	return &service.Account{ID: id, Name: "test", Status: service.StatusActive, Schedulable: s.enabled[id]}, nil
}
func (s *oneClickAccountService) SetAccountSchedulable(ctx context.Context, id int64, enabled bool) (*service.Account, error) {
	s.enabled[id] = enabled
	s.writes = append(s.writes, id)
	return s.GetAccount(ctx, id)
}
func TestOneClickSchedulingSwitchPersistsOnlyTargetAccount(t *testing.T) {
	gin.SetMode(gin.TestMode)
	svc := &oneClickAccountService{stubAdminService: &stubAdminService{}, enabled: map[int64]bool{1: true, 2: true}}
	oldControl := &schedulingAdminStub{}
	h := NewAccountHandler(svc, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
	h.SetSchedulingController(oldControl)
	router := gin.New()
	router.POST("/accounts/:id/schedulable", h.SetSchedulable)
	for _, enabled := range []bool{false, true, false} {
		body, _ := json.Marshal(map[string]bool{"schedulable": enabled})
		w := schedulingTestCall(router, http.MethodPost, "/accounts/1/schedulable", string(body))
		require.Equal(t, http.StatusOK, w.Code, w.Body.String())
		var response struct{ Data struct{ Schedulable bool } }
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &response))
		t.Logf("requested=%v persisted_target=%v response=%v other=%v old_control_writes=%d", enabled, svc.enabled[1], response.Data.Schedulable, svc.enabled[2], oldControl.writes)
		require.Equal(t, enabled, svc.enabled[1], "the original switch must persist its value")
		require.Equal(t, enabled, response.Data.Schedulable)
		require.True(t, svc.enabled[2], "another account is outside this operation")
	}
	require.Equal(t, []int64{1, 1, 1}, svc.writes)
	require.Zero(t, oldControl.writes, "retired pause/resume is not a second switch")
}
func TestOneClickSchedulingSwitchRejectsAmbiguousInput(t *testing.T) {
	gin.SetMode(gin.TestMode)
	svc := &oneClickAccountService{stubAdminService: &stubAdminService{}, enabled: map[int64]bool{1: true}}
	h := NewAccountHandler(svc, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
	router := gin.New()
	router.POST("/accounts/:id/schedulable", h.SetSchedulable)
	for _, body := range []string{"{}", "null", "{\"schedulable\":null}", "{\"schedulable\":\"false\"}", "{\"schedulable\":false,\"scope\":\"credential_family\"}", "{\"schedulable\":false}{\"schedulable\":true}"} {
		w := schedulingTestCall(router, http.MethodPost, "/accounts/1/schedulable", body)
		require.Equal(t, http.StatusBadRequest, w.Code, body)
		require.True(t, svc.enabled[1], body)
		require.Empty(t, svc.writes, body)
	}
}
