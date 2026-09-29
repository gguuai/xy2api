//go:build unit

package service

import (
	"context"
	"database/sql"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"

	"github.com/gin-gonic/gin"
	"strings"
	"testing"
	"time"

	"github.com/liulixin-lex/xy2api/internal/scheduling"
	"github.com/stretchr/testify/require"
)

func TestControlledCallerDeadlineDoesNotWaitForTerminalStoreLock(t *testing.T) {
	s, db, _, accounts := controlledIntegration(t, true)
	ctx, r := controlledIntegrationRequest(t, s)
	a := controlledPick(t, s, ctx, r, accounts[:1])
	req, err := http.NewRequestWithContext(ctx, "POST", "http://unused", strings.NewReader("{\"model\":\"test-model\",\"stream\":false}"))
	require.NoError(t, err)
	original := req.GetBody
	reads := 0
	var lock *sql.Tx
	req.GetBody = func() (io.ReadCloser, error) {
		reads++
		if reads == 2 {
			var e error
			lock, e = db.Begin()
			require.NoError(t, e)
			t.Cleanup(func() { _ = lock.Rollback() })
			_, e = lock.Exec("LOCK TABLE scheduling_attempts IN ACCESS EXCLUSIVE MODE")
			require.NoError(t, e)
			controlledShortBudget(r, 150*time.Millisecond)
		}
		return original()
	}
	sends := 0
	started := time.Now()
	_, err = s.roundTrip(req, a.ID, 10, func(*http.Request) (*http.Response, error) { sends++; return nil, nil })
	elapsed := time.Since(started)
	require.ErrorIs(t, err, scheduling.ErrDeadline)
	require.NotNil(t, lock)
	require.Less(t, elapsed, time.Second, "caller must return while the terminal table lock is still held")
	require.Zero(t, sends)
	require.Zero(t, r.Ledger.Snapshot().Attempts)
	var active int
	require.NoError(t, lock.QueryRow("SELECT COUNT(*) FROM scheduling_attempts WHERE request_id=$1 AND state<>'settled'", r.ID).Scan(&active))
	require.Equal(t, 1, active, "the unresolved PG ticket must retain capacity")
	closeStarted := time.Now()
	r.Close()
	require.Less(t, time.Since(closeStarted), 100*time.Millisecond, "Close must not wait for asynchronous Finish's Once")
	require.NoError(t, lock.Rollback())
	require.Eventually(t, func() bool {
		var count int
		return db.QueryRow("SELECT COUNT(*) FROM scheduling_attempts WHERE request_id=$1 AND state='settled' AND outcome='not_sent'", r.ID).Scan(&count) == nil && count == 1
	}, 3*time.Second, 10*time.Millisecond)
	t.Logf("roundTrip=%s held_lock_active=1 actual_attempts=0 sends=0 settled_not_sent=1", elapsed)
}

func seedControlledActualModelHealth(t *testing.T, s *ControlledSchedulingService, r *ControlledRequest, a *Account, model string, state scheduling.HealthState) scheduling.HealthSnapshot {
	t.Helper()
	snapshot := scheduling.HealthSnapshot{Generation: "fixture-" + model, State: state, StageRevision: 1}
	if state == scheduling.HealthOpen {
		snapshot.CooldownUntilMS = time.Now().Add(time.Minute).UnixMilli()
	}
	raw, err := json.Marshal(snapshot)
	require.NoError(t, err)
	identity, err := s.controlledHealthIdentity(context.Background(), a)
	require.NoError(t, err)
	key := scheduling.HealthRedisKey(a.ID, model, r.Profile, r.Reasoning, controlledBucket(r), r.Protocol, identity)
	require.NoError(t, s.redis.HSet(context.Background(), key, "snapshot", raw).Err())
	return snapshot
}

func TestControlledFinalModelRevalidatesHealthAndProbeCapacity(t *testing.T) {
	for _, blocked := range []bool{false, true} {
		name := "half_open_single_probe"
		if blocked {
			name = "open_rejected"
		}
		t.Run(name, func(t *testing.T) {
			s, db, _, accounts := controlledIntegration(t, true)
			a := accounts[0]
			a.Credentials = map[string]any{"model_mapping": map[string]any{"test-model": "predicted-A"}}
			raw, err := json.Marshal(a.Credentials)
			require.NoError(t, err)
			_, err = db.Exec("UPDATE accounts SET credentials=$1 WHERE id=$2", string(raw), a.ID)
			require.NoError(t, err)
			require.Equal(t, "predicted-A", a.GetMappedModel("test-model"))
			ctx, r := controlledIntegrationRequest(t, s)
			seedControlledActualModelHealth(t, s, r, a, "predicted-A", scheduling.HealthHealthy)
			if blocked {
				seedControlledActualModelHealth(t, s, r, a, "actual-B", scheduling.HealthOpen)
			} else {
				seedControlledActualModelHealth(t, s, r, a, "actual-B", scheduling.HealthHalfOpen)
			}
			controlledPick(t, s, ctx, r, accounts[:1])
			require.Equal(t, "predicted-A", r.Decision.HealthFence.Model)
			request := func(ctx context.Context) *http.Request {
				req, e := http.NewRequestWithContext(ctx, "POST", "http://unused", strings.NewReader("{\"model\":\"actual-B\",\"stream\":false}"))
				require.NoError(t, e)
				return req
			}
			sends := 0
			response, err := s.roundTrip(request(ctx), a.ID, 10, func(req *http.Request) (*http.Response, error) {
				sends++
				d := req.Context().Value(controlledDispatchContextKey{}).(*controlledDispatch)
				require.Equal(t, "actual-B", d.healthObservation.Model)
				require.Equal(t, "actual-B", d.healthObservation.Fence.Model)
				require.True(t, d.decision.Probe, "predicted HEALTHY cannot bypass actual HALF_OPEN capacity")
				require.Equal(t, scheduling.HealthHalfOpen, d.healthObservation.Fence.State)
				ctx2, r2 := controlledIntegrationRequest(t, s)
				controlledPick(t, s, ctx2, r2, accounts[:1])
				_, secondErr := s.roundTrip(request(ctx2), a.ID, 10, func(*http.Request) (*http.Response, error) {
					t.Fatal("second HALF_OPEN probe must not be sent")
					return nil, nil
				})
				require.Error(t, secondErr)
				var active int
				require.NoError(t, db.QueryRow("SELECT COUNT(*) FROM scheduling_attempts WHERE state<>'settled'").Scan(&active))
				require.Equal(t, 1, active)
				require.Zero(t, r2.Ledger.Snapshot().Attempts)
				return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader("{\"object\":\"response\",\"status\":\"completed\",\"output\":[],\"usage\":{\"input_tokens\":1,\"output_tokens\":1}}"))}, nil
			})
			if blocked {
				require.Error(t, err)
				require.Zero(t, sends)
				require.Zero(t, r.Ledger.Snapshot().Attempts)
				var tickets int
				require.NoError(t, db.QueryRow("SELECT COUNT(*) FROM scheduling_attempts").Scan(&tickets))
				require.Zero(t, tickets)
				peer := controlledPick(t, s, ctx, r, accounts)
				require.NotEqual(t, a.ID, peer.ID, "actual-model rejection must not repeatedly select the same healthy alias")
				require.Zero(t, r.Ledger.Snapshot().Attempts, "unsent rejection cannot consume an upstream attempt")
				return
			}
			require.NoError(t, err)
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			c.Request = httptest.NewRequest("POST", "/v1/responses", nil).WithContext(ctx)
			_, err = (&OpenAIGatewayService{}).handleNonStreamingResponse(ctx, response, c, a, "actual-B", "actual-B")
			require.NoError(t, err)
			require.NoError(t, response.Body.Close())
			require.Equal(t, 1, sends)
			identity, e := s.controlledHealthIdentity(ctx, a)
			require.NoError(t, e)
			for _, model := range []string{"predicted-A", "actual-B"} {
				key := scheduling.HealthRedisKey(a.ID, model, r.Profile, r.Reasoning, controlledBucket(r), r.Protocol, identity)
				raw, e := s.redis.HGet(ctx, key, "snapshot").Bytes()
				require.NoError(t, e)
				var h scheduling.HealthSnapshot
				require.NoError(t, json.Unmarshal(raw, &h))
				if model == "predicted-A" {
					require.Equal(t, scheduling.HealthHealthy, h.State)
					require.Zero(t, h.GoodStreak)
				} else {
					require.Equal(t, 1, h.GoodStreak)
				}
			}
		})
	}
}
