package admin

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/liulixin-lex/xy2api/internal/scheduling"
	"github.com/stretchr/testify/require"
)

type groupSchedulingAdminStub struct {
	schedulingAdminStub
	record                  scheduling.GroupPolicyRecord
	groupReads, groupWrites int
	received                scheduling.GroupPolicy
	expected                int64
}

func (s *groupSchedulingAdminStub) GetGroupPolicy(_ context.Context, id int64) (scheduling.GroupPolicyRecord, error) {
	s.groupReads++
	return s.record, s.err
}
func (s *groupSchedulingAdminStub) PutGroupPolicy(_ context.Context, p scheduling.GroupPolicy, expected int64) (scheduling.GroupPolicyRecord, error) {
	s.groupWrites++
	s.received = p
	s.expected = expected
	return s.record, s.err
}
func groupSchedulingRouter(s *groupSchedulingAdminStub) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	h := NewSchedulingHandler(s, nil)
	r.GET("/groups/:id", h.GetGroupPolicy)
	r.PUT("/groups/:id", h.PutGroupPolicy)
	r.Any("/policies", h.RetiredModelPolicy)
	return r
}
func validGroupPolicyJSON(t *testing.T) string {
	t.Helper()
	p := scheduling.DefaultGroupPolicy(10)
	priority := 0
	p.Accounts = []scheduling.AccountRule{{AccountID: 1, Priority: &priority, Weight: 0}}
	raw, err := json.Marshal(map[string]any{"expected_version": 0, "policy": p})
	require.NoError(t, err)
	return string(raw)
}
func TestGroupSchedulingHandlerReadAndSaveContract(t *testing.T) {
	p := scheduling.DefaultGroupPolicy(10)
	p.Version = 1
	s := &groupSchedulingAdminStub{record: scheduling.GroupPolicyRecord{GroupID: 10, Version: 1, Configured: true, DefaultScope: "group", Policy: p, MigrationWarnings: []scheduling.GroupPolicyWarning{}}}
	r := groupSchedulingRouter(s)
	got := schedulingTestCall(r, http.MethodGet, "/groups/10", "")
	require.Equal(t, 200, got.Code)
	var envelope struct {
		Data scheduling.GroupPolicyRecord `json:"data"`
	}
	require.NoError(t, json.Unmarshal(got.Body.Bytes(), &envelope))
	require.Equal(t, s.record, envelope.Data)
	saved := schedulingTestCall(r, http.MethodPut, "/groups/10", validGroupPolicyJSON(t))
	require.Equal(t, 200, saved.Code, saved.Body.String())
	require.Equal(t, 1, s.groupWrites)
	require.EqualValues(t, 0, s.expected)
	require.EqualValues(t, 10, s.received.GroupID)
	require.EqualValues(t, 0, *s.received.Accounts[0].Priority)
	require.EqualValues(t, 0, s.received.Accounts[0].Weight)
}
func TestGroupSchedulingHandlerRejectsIncompleteOrCrossGroupInput(t *testing.T) {
	good := validGroupPolicyJSON(t)
	for _, tc := range []struct{ name, body string }{
		{"group_mismatch", strings.Replace(good, `"group_id":10`, `"group_id":20`, 1)},
		{"missing_priority", strings.Replace(good, `"priority":0,`, "", 1)},
		{"null_priority", strings.Replace(good, `"priority":0`, `"priority":null`, 1)},
		{"missing_weight", strings.Replace(good, `"traffic_weight":0,`, "", 1)},
		{"fractional_weight", strings.Replace(good, `"traffic_weight":0`, `"traffic_weight":0.5`, 1)},
		{"old_model", strings.Replace(good, `"group_id":10`, `"group_id":10,"model":"m"`, 1)},
		{"missing_version", strings.Replace(good, `"expected_version":0,`, "", 1)},
		{"negative_version", strings.Replace(good, `"expected_version":0`, `"expected_version":-1`, 1)},
		{"no_policy", `{"expected_version":0}`},
		{"trailing_object", good + `{}`},
		{"short_timeout", strings.Replace(good, `"first_output_timeout_ms":120000`, `"first_output_timeout_ms":0`, 1)},
		{"too_many_attempts", strings.Replace(good, `"max_attempts":3`, `"max_attempts":11`, 1)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := &groupSchedulingAdminStub{}
			w := schedulingTestCall(groupSchedulingRouter(s), http.MethodPut, "/groups/10", tc.body)
			require.Equal(t, 400, w.Code, w.Body.String())
			require.Zero(t, s.groupWrites)
		})
	}
}
func TestGroupSchedulingHandlerErrorStatusAndRetirement(t *testing.T) {
	for _, tc := range []struct {
		err  error
		want int
	}{{scheduling.ErrVersionConflict, 409}, {scheduling.ErrSchedulingGroupNotFound, 404}, {scheduling.ErrInvalidControl, 400}, {scheduling.ErrSharedState, 503}} {
		s := &groupSchedulingAdminStub{}
		s.err = tc.err
		w := schedulingTestCall(groupSchedulingRouter(s), http.MethodPut, "/groups/10", validGroupPolicyJSON(t))
		require.Equal(t, tc.want, w.Code)
	}
	s := &groupSchedulingAdminStub{}
	r := groupSchedulingRouter(s)
	for _, method := range []string{http.MethodGet, http.MethodPut, http.MethodDelete} {
		w := schedulingTestCall(r, method, "/policies", validGroupPolicyJSON(t))
		require.Equal(t, 410, w.Code)
	}
	require.Zero(t, s.reads)
	require.Zero(t, s.writes)
	require.Zero(t, s.groupWrites)
}
