//go:build unit

package service

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/liulixin-lex/xy2api/internal/scheduling"
	"github.com/stretchr/testify/require"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

func controlledShortBudget(r *ControlledRequest, duration time.Duration) {
	r.mu.Lock()
	p := r.Profile
	p.TotalBudgetMS = duration.Milliseconds()
	r.Ledger = scheduling.NewAttemptLedger(r.Policy.Retry, p, time.Now(), time.Time{})
	r.mu.Unlock()
}

func TestControlledPreparationDeadlineBoundsStoreWaits(t *testing.T) {
	for _, phase := range []string{"selection", "admission", "send_record"} {
		t.Run(phase, func(t *testing.T) {
			s, db, _, accounts := controlledIntegration(t, true)
			ctx, r := controlledIntegrationRequest(t, s)
			var d *controlledDispatch
			account := accounts[0]
			if phase != "selection" {
				account = controlledPick(t, s, ctx, r, accounts)
			}
			if phase == "send_record" {
				var err error
				d, err = s.beginDispatch(ctx, account.ID, 10)
				require.NoError(t, err)
			}
			table := "scheduling_attempts"
			if phase == "admission" {
				table = "scheduling_controls"
			}
			tx, err := db.Begin()
			require.NoError(t, err)
			t.Cleanup(func() { _ = tx.Rollback() })
			_, err = tx.Exec("LOCK TABLE " + table + " IN ACCESS EXCLUSIVE MODE")
			require.NoError(t, err)
			controlledShortBudget(r, 150*time.Millisecond)
			started := time.Now()
			switch phase {
			case "selection":
				_, err = s.selectAccount(ctx, r, accounts, func(*Account) (bool, string) { return true, "test" }, nil, false)
			case "admission":
				_, err = s.beginDispatch(ctx, account.ID, 10)
			case "send_record":
				err = d.MarkSent()
			}
			elapsed := time.Since(started)
			require.NoError(t, tx.Rollback())
			if d != nil {
				d.Finish("not_sent", true, err)
			}
			require.ErrorIs(t, err, scheduling.ErrDeadline)
			require.Less(t, elapsed, 2*time.Second)
			require.Zero(t, r.Ledger.Snapshot().Attempts)
			var active int
			require.NoError(t, db.QueryRow("SELECT COUNT(*) FROM scheduling_attempts WHERE state<>'settled'").Scan(&active))
			require.Zero(t, active)
			t.Logf("phase=%s elapsed=%s actual_attempts=0 active=0", phase, elapsed)
		})
	}
}

func TestControlledPreparationDeadlineDoesNotCancelCompletedLongStream(t *testing.T) {
	s, _, _, accounts := controlledIntegration(t, true)
	ctx, r := controlledIntegrationRequest(t, s)
	a := controlledPick(t, s, ctx, r, accounts)
	controlledShortBudget(r, 200*time.Millisecond)
	response, err := controlledHTTP(t, s, ctx, a.ID, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"type\":\"response.output_text.delta\",\"delta\":\"hello\"}\n\n")
		w.(http.Flusher).Flush()
		time.Sleep(350 * time.Millisecond)
		fmt.Fprint(w, "data: {\"type\":\"response.completed\"}\n\n")
	})
	require.NoError(t, err)
	// The transport stages the first semantic frame; model the adapter flushing
	// that frame to the client before consuming the rest of the long answer.
	r.markSemantic(time.Now(), true)
	raw, err := io.ReadAll(response.Body)
	require.NoError(t, err)
	require.NoError(t, response.Body.Close())
	require.Contains(t, string(raw), "response.completed")
	require.False(t, r.semanticAt.IsZero())
	require.True(t, r.Ledger.Snapshot().Committed)
	require.True(t, time.Now().After(r.Ledger.Snapshot().Deadline))
}

func TestControlledHTTPAuthenticationTerminalNeverRecoversHealth(t *testing.T) {
	s, db, _, accounts := controlledIntegration(t, true)
	ctx, r := controlledIntegrationRequest(t, s)
	a := controlledPick(t, s, ctx, r, accounts)
	req, err := http.NewRequestWithContext(ctx, "POST", "http://unused", strings.NewReader("{\"model\":\"test-model\",\"stream\":false}"))
	require.NoError(t, err)
	response, err := s.roundTrip(req, a.ID, 10, func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 401, Header: http.Header{}, Body: io.NopCloser(strings.NewReader("{\"error\":{\"code\":\"invalid_api_key\"}}"))}, nil
	})
	require.NoError(t, err)
	controlledConsume(t, response)
	var outcome string
	require.NoError(t, db.QueryRow("SELECT outcome FROM scheduling_attempts WHERE request_id=$1", r.ID).Scan(&outcome))
	require.Equal(t, "http_error", outcome)
	raw, err := s.redis.HGet(context.Background(), scheduling.HealthRedisKey(a.ID, r.Policy.Model, r.Profile, r.Reasoning, controlledBucket(r), r.Protocol, scheduling.StableHealthIdentity(a.Platform, a.Type, a.Credentials, a.Extra)), "snapshot").Bytes()
	require.NoError(t, err)
	var health scheduling.HealthSnapshot
	require.NoError(t, json.Unmarshal(raw, &health))
	require.Zero(t, health.GoodStreak)
	require.NotEqual(t, scheduling.HealthHealthy, health.State)
}

func TestControlledPreparationPreservesClientCancellation(t *testing.T) {
	parent, cancel := context.WithCancel(context.Background())
	bounded, stop := context.WithDeadline(parent, time.Now().Add(-time.Second))
	defer stop()
	cancel()
	require.ErrorIs(t, controlledPreparationError(parent, bounded, context.Canceled), context.Canceled)
}

func TestControlledFailureModelFallbackUsesMappingAndDeadline(t *testing.T) {
	s, db, _, accounts := controlledIntegration(t, true)
	ctx, r := controlledIntegrationRequest(t, s)
	accounts[0].Credentials = map[string]any{"model_mapping": map[string]any{"test-model": "actual-model"}}
	_, err := db.Exec("UPDATE accounts SET credentials=$1::jsonb WHERE id=1", "{\"model_mapping\":{\"test-model\":\"actual-model\"}}")
	require.NoError(t, err)
	req, err := http.NewRequestWithContext(ctx, "POST", "http://unused", strings.NewReader("{}"))
	require.NoError(t, err)
	prepared, err := s.prepareControlledFailureRequest(req, 1)
	require.NoError(t, err)
	require.Equal(t, "actual-model", controlledFailureAdmission(prepared.Context()).Model)
	require.Same(t, r, controlledRequest(prepared.Context()))
	r.mu.Lock()
	r.Ledger = scheduling.NewAttemptLedger(r.Policy.Retry, r.Profile, time.Now().Add(-time.Second), time.Now().Add(-time.Millisecond))
	r.mu.Unlock()
	_, err = s.prepareControlledFailureRequest(req, 1)
	require.ErrorIs(t, err, scheduling.ErrDeadline)
	require.Zero(t, r.Ledger.Snapshot().Attempts)
}
