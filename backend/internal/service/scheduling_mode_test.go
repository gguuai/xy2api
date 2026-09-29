package service

import (
	"context"
	"database/sql"
	"errors"
	"github.com/DATA-DOG/go-sqlmock"
	"github.com/gin-gonic/gin"
	"github.com/liulixin-lex/xy2api/internal/scheduling"
	"github.com/stretchr/testify/require"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestSchedulingModeRequestSnapshotDoesNotFollowLiveSwitch(t *testing.T) {
	calls := 0
	mode := scheduling.ModeSub2API
	svc := &ControlledSchedulingService{modeReader: func(context.Context) (scheduling.ModeSnapshot, error) {
		calls++
		return scheduling.ModeSnapshot{Mode: mode, Version: int64(calls)}, nil
	}}
	ctx := NewControlledRequestContext(context.Background(), "responses")
	r, enabled, err := svc.loadPolicy(ctx, nil, "model-a", "")
	require.NoError(t, err)
	require.False(t, enabled)
	require.True(t, Sub2APISchedulingEnabled(ctx))
	require.Nil(t, r.Ledger)
	mode = scheduling.ModeControlled
	_, enabled, err = svc.loadPolicy(ctx, nil, "model-b", "")
	require.NoError(t, err)
	require.False(t, enabled)
	require.Equal(t, 1, calls)
	d, err := svc.beginDispatch(ctx, 1, 2)
	require.NoError(t, err)
	require.Nil(t, d)
}
func TestSchedulingModeControlledStoreFailureNeverFallsBack(t *testing.T) {
	svc := &ControlledSchedulingService{modeReader: func(context.Context) (scheduling.ModeSnapshot, error) {
		return scheduling.ModeSnapshot{}, errors.New("storage offline")
	}}
	ctx := NewControlledRequestContext(context.Background(), "responses")
	_, enabled, err := svc.loadPolicy(ctx, nil, "model", "")
	require.ErrorIs(t, err, scheduling.ErrSharedState)
	require.False(t, enabled)
	require.False(t, Sub2APISchedulingEnabled(ctx))
}
func TestSchedulingModeWebSocketNewTurnRefreshesWithoutChangingOldTurn(t *testing.T) {
	db, m, err := sqlmock.New()
	require.NoError(t, err)
	defer func() { _ = db.Close() }()
	mode := scheduling.ModeSub2API
	reads := 0
	svc := &OpenAIGatewayService{controlledScheduling: &ControlledSchedulingService{Store: scheduling.NewPostgresStore(db), modeReader: func(context.Context) (scheduling.ModeSnapshot, error) {
		reads++
		return scheduling.ModeSnapshot{Mode: mode, Version: int64(reads)}, nil
	}}}
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest("GET", "/v1/responses", nil)
	body := []byte("{\"model\":\"m\"}")
	first, err := svc.prepareControlledWSTurn(context.Background(), c, body, "m", "owner-session", false)
	require.NoError(t, err)
	require.True(t, Sub2APISchedulingEnabled(first))
	mode = scheduling.ModeControlled
	retry, err := svc.prepareControlledWSTurn(first, c, body, "m", "", false)
	require.NoError(t, err)
	require.Same(t, controlledRequest(first), controlledRequest(retry))
	require.Equal(t, 1, reads)
	m.ExpectQuery("SELECT version,policy FROM scheduling_group_policies").WithArgs(int64(0)).WillReturnError(sql.ErrNoRows)
	next, err := svc.prepareControlledWSTurn(first, c, body, "m", "", true)
	require.NoError(t, err)
	require.True(t, ControlledSchedulingEnabled(next))
	require.True(t, Sub2APISchedulingEnabled(first))
	require.Equal(t, 2, reads)
	require.NoError(t, m.ExpectationsWereMet())
}
func TestSchedulingModeMiddlewarePreservesSub2APIBodyAndWriter(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(ControlledSchedulingMiddleware(func(context.Context) (scheduling.ModeSnapshot, error) {
		return scheduling.ModeSnapshot{Mode: scheduling.ModeSub2API, Version: 2}, nil
	}))
	r.POST("/v1/responses", func(c *gin.Context) {
		require.True(t, Sub2APISchedulingEnabled(c.Request.Context()))
		_, wrapped := c.Writer.(*schedulingResponseWriter)
		require.False(t, wrapped)
		_, wrappedBody := c.Request.Body.(*schedulingMetadataBody)
		require.False(t, wrappedBody)
		raw, err := io.ReadAll(c.Request.Body)
		require.NoError(t, err)
		c.Data(200, "application/json", raw)
	})
	raw := "{\"model\":\"m\",\"input\":\"hello\"}"
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(raw)))
	require.Equal(t, 200, w.Code)
	require.Equal(t, raw, w.Body.String())
	require.Empty(t, w.Header().Get("X-Scheduling-Request-Id"))
}
func TestSchedulingModeMiddlewareFailureDoesNotDispatch(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	called := false
	r.Use(ControlledSchedulingMiddleware(func(context.Context) (scheduling.ModeSnapshot, error) {
		return scheduling.ModeSnapshot{}, errors.New("offline")
	}))
	r.GET("/v1/models", func(c *gin.Context) { called = true; c.Status(200) })
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/v1/models", nil))
	require.Equal(t, 503, w.Code)
	require.False(t, called)
}

func TestSchedulingModeSub2APITurnDoesNotInheritQualityRouting(t *testing.T) {
	q := &openAIQualityRequest{scope: "previous-controlled-session"}
	ctx := context.WithValue(context.Background(), openAIQualityContextKey{}, q)
	ctx = NewControlledRequestContext(ctx, "ws")
	r := controlledRequest(ctx)
	r.modeResolved = true
	r.Mode = scheduling.ModeSnapshot{Mode: scheduling.ModeSub2API, Version: 9}
	require.Nil(t, qualityRequest(ctx))
	next := NewControlledRequestContext(ctx, "ws")
	n := controlledRequest(next)
	n.modeResolved = true
	n.Mode = scheduling.ModeSnapshot{Mode: scheduling.ModeControlled, Version: 10}
	require.Same(t, q, qualityRequest(next))
}
