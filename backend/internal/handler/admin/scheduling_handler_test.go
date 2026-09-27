package admin

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/liulixin-lex/xy2api/internal/scheduling"
	"github.com/stretchr/testify/require"
)

type schedulingAdminStub struct {
	writes   int
	reads    int
	commands []scheduling.ControlCommand
	err      error
}

func (s *schedulingAdminStub) GetPolicy(context.Context, int64, string) (scheduling.PolicyRecord, error) {
	s.reads++
	return scheduling.PolicyRecord{Version: 7}, s.err
}
func (s *schedulingAdminStub) PutPolicy(_ context.Context, p scheduling.Policy, _ int64) (scheduling.PolicyRecord, error) {
	s.writes++
	return scheduling.PolicyRecord{Policy: &p, Version: 8}, s.err
}
func (s *schedulingAdminStub) GetControl(context.Context, int64, string) (scheduling.ControlSnapshot, error) {
	s.reads++
	return scheduling.ControlSnapshot{State: scheduling.ControlDraining, ActiveAttempts: 2}, s.err
}
func (s *schedulingAdminStub) Control(_ context.Context, c scheduling.ControlCommand) (scheduling.ControlSnapshot, error) {
	s.writes++
	s.commands = append(s.commands, c)
	return scheduling.ControlSnapshot{State: scheduling.ControlPaused, Epoch: 1}, s.err
}
func (s *schedulingAdminStub) ListRequestAttempts(_ context.Context, requestID string) ([]scheduling.AttemptRecord, error) {
	s.reads++
	return []scheduling.AttemptRecord{{TicketID: "ticket", RequestID: requestID, AccountID: 7, Metrics: json.RawMessage("{}")}}, s.err
}
func (s *schedulingAdminStub) DispatchStatistics(_ context.Context, groupID int64, model string, since, until time.Time) (scheduling.DispatchStats, error) {
	s.reads++
	return scheduling.DispatchStats{GroupID: groupID, Model: model, Since: since, Until: until, Accounts: []scheduling.AccountDispatchStats{}}, s.err
}
func schedulingTestRouter(s *schedulingAdminStub, explain SchedulingExplainFunc) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	h := NewSchedulingHandler(s, explain)
	r.GET("/policies", h.GetPolicy)
	r.GET("/stats", h.GetStatistics)
	r.GET("/requests/:request_id/attempts", h.ListRequestAttempts)
	r.PUT("/policies", h.PutPolicy)
	r.POST("/explain", h.Explain)
	r.GET("/accounts/:id/control", h.GetControl)
	r.POST("/accounts/:id/control", h.Control)
	return r
}
func schedulingTestCall(r *gin.Engine, method, path, body string) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)
	return w
}
func TestSchedulingHandlerExplainIsReadOnly(t *testing.T) {
	s := &schedulingAdminStub{}
	calls := 0
	r := schedulingTestRouter(s, func(_ context.Context, p json.RawMessage) (any, error) {
		calls++
		require.JSONEq(t, `{"model":"m"}`, string(p))
		return map[string]any{"selected_account": 4}, nil
	})
	w := schedulingTestCall(r, "POST", "/explain", `{"model":"m"}`)
	require.Equal(t, http.StatusOK, w.Code)
	require.Equal(t, 1, calls)
	require.Zero(t, s.writes)
	require.Zero(t, s.reads)
}
func TestSchedulingHandlerRequiresVersions(t *testing.T) {
	s := &schedulingAdminStub{}
	r := schedulingTestRouter(s, nil)
	w := schedulingTestCall(r, "POST", "/accounts/1/control", `{"action":"pause"}`)
	require.Equal(t, http.StatusBadRequest, w.Code)
	w = schedulingTestCall(r, "PUT", "/policies", `{"group_id":0,"model":"m","policy":{}}`)
	require.Equal(t, http.StatusBadRequest, w.Code)
	require.Zero(t, s.writes)
}
func TestSchedulingHandlerControlCASAndEnvelope(t *testing.T) {
	s := &schedulingAdminStub{}
	r := schedulingTestRouter(s, nil)
	w := schedulingTestCall(r, "POST", "/accounts/2/control", `{"action":"pause","scope":"credential_family","expected_epoch":0}`)
	require.Equal(t, http.StatusOK, w.Code)
	require.JSONEq(t, `{"code":0,"message":"success","data":{"account_id":0,"subject_id":0,"scope":"","state":"PAUSED","epoch":1,"mode":"","active_attempts":0,"unknown_attempts":0,"allowed_sessions":0,"pending_settlements":0,"updated_at":"0001-01-01T00:00:00Z","session_deadline":null,"session_turn_limit":0}}`, w.Body.String())
	require.EqualValues(t, 2, s.commands[0].AccountID)
	require.Equal(t, scheduling.ScopeFamily, s.commands[0].Scope)
	s.err = scheduling.ErrVersionConflict
	w = schedulingTestCall(r, "POST", "/accounts/2/control", `{"action":"resume","expected_epoch":0}`)
	require.Equal(t, http.StatusConflict, w.Code)
}
func TestSchedulingHandlerReadsNoWrites(t *testing.T) {
	s := &schedulingAdminStub{}
	r := schedulingTestRouter(s, nil)
	require.Equal(t, http.StatusOK, schedulingTestCall(r, "GET", "/policies?group_id=1&model=m", "").Code)
	require.Equal(t, http.StatusOK, schedulingTestCall(r, "GET", "/accounts/1/control", "").Code)
	require.Equal(t, 2, s.reads)
	require.Zero(t, s.writes)
}
func TestSchedulingLegacyAndBulkControlsUseGate(t *testing.T) {
	gin.SetMode(gin.TestMode)
	s := &schedulingAdminStub{}
	svc := &stubAdminService{}
	h := NewAccountHandler(svc, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
	h.SetSchedulingController(s)
	r := gin.New()
	r.POST("/accounts/:id/schedulable", h.SetSchedulable)
	r.POST("/bulk", h.BulkUpdate)
	w := schedulingTestCall(r, "POST", "/accounts/7/schedulable", `{"schedulable":false}`)
	require.Equal(t, http.StatusOK, w.Code)
	require.Equal(t, "pause", s.commands[0].Action)
	w = schedulingTestCall(r, "POST", "/bulk", `{"account_ids":[1,2],"schedulable":true}`)
	require.Equal(t, http.StatusOK, w.Code)
	require.Nil(t, svc.lastBulkUpdateAccountInput.Schedulable)
	require.Equal(t, 3, s.writes)
	require.Equal(t, "resume", s.commands[1].Action)
	require.Equal(t, "resume", s.commands[2].Action)
}

func TestSchedulingHandlerRequestAttemptsIsReadOnly(t *testing.T) {
	s := &schedulingAdminStub{}
	r := schedulingTestRouter(s, nil)
	w := schedulingTestCall(r, "GET", "/requests/request-123/attempts", "")
	require.Equal(t, http.StatusOK, w.Code)
	var body struct {
		Data struct {
			RequestID string                     `json:"request_id"`
			Attempts  []scheduling.AttemptRecord `json:"attempts"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
	require.Equal(t, "request-123", body.Data.RequestID)
	require.Len(t, body.Data.Attempts, 1)
	require.EqualValues(t, 7, body.Data.Attempts[0].AccountID)
	require.Equal(t, 1, s.reads)
	require.Zero(t, s.writes)
	s.err = scheduling.ErrSharedState
	require.Equal(t, http.StatusServiceUnavailable, schedulingTestCall(r, "GET", "/requests/request-123/attempts", "").Code)
}

func TestSchedulingHandlerStatisticsIsReadOnly(t *testing.T) {
	s := &schedulingAdminStub{}
	r := schedulingTestRouter(s, nil)
	w := schedulingTestCall(r, "GET", "/stats?group_id=7&model=m&since=2020-01-01T00:00:00Z", "")
	require.Equal(t, http.StatusOK, w.Code)
	require.Equal(t, 1, s.reads)
	require.Zero(t, s.writes)
	require.Equal(t, http.StatusBadRequest, schedulingTestCall(r, "GET", "/stats?model=m&since=invalid", "").Code)
	require.Equal(t, 1, s.reads)
}
