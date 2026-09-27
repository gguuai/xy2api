package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/liulixin-lex/xy2api/internal/scheduling"
	"github.com/stretchr/testify/require"
)

// A fully completed no-argument Chat tool call is semantic output even when
// no function.arguments delta was emitted. The upstream timer must see it.
func TestIndependentReviewChatNoArgumentToolCompletesSemantic(t *testing.T) {
	d := &controlledDispatch{request: &ControlledRequest{}, semanticReady: make(chan struct{})}
	d.ObserveFrame([]byte("{\"choices\":[{\"delta\":{\"tool_calls\":[{\"index\":0,\"id\":\"call_1\",\"function\":{\"name\":\"get_time\",\"arguments\":\"\"}}]}}]}"))
	d.ObserveFrame([]byte("{\"choices\":[{\"delta\":{},\"finish_reason\":\"tool_calls\"}]}"))
	require.False(t, d.semantic.IsZero(), "complete no-argument tool call must stop the first-semantic timer")
	require.True(t, d.answer.IsZero(), "tool output is not prose")
}

// PG acknowledgement fails before any transport is called. Such a ticket must
// stay not_sent and must not consume the logical request's actual-attempt count.
func TestIndependentReviewFailedDispatchRecordDoesNotCountSend(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer func() { _ = db.Close() }()
	mock.ExpectExec("UPDATE scheduling_attempts").WillReturnError(errors.New("injected dispatch metrics write failure"))
	policy := scheduling.NormalizePolicy(scheduling.Policy{Enabled: true, Model: "review"})
	request := &ControlledRequest{Policy: policy, ReplaySafe: true, Started: time.Now(), Ledger: scheduling.NewAttemptLedger(policy.Retry, scheduling.LatencyProfile{}, time.Now(), time.Time{})}
	d := &controlledDispatch{service: &ControlledSchedulingService{Store: scheduling.NewPostgresStore(db)}, request: request, ticket: scheduling.DispatchTicket{TicketID: "review-ticket", AccountID: 1}, decision: scheduling.Decision{AccountID: 1}, ctx: context.Background()}
	err = d.MarkSent()
	require.Error(t, err)
	require.NoError(t, mock.ExpectationsWereMet())
	require.False(t, d.sent, "no upstream call occurred after the PG write failed")
	require.Zero(t, request.Ledger.Snapshot().Attempts, "pre-send failure must not consume an actual attempt")
}

func TestControlledOwnerConstraintSurvivesLegacyRollback(t *testing.T) {
	ctx := NewControlledRequestContext(context.Background(), "responses")
	r := controlledRequest(ctx)
	r.policyLoaded = true
	r.Policy.Enabled = false
	r.owner, r.ownerAccountID = true, 1
	// No store is configured: a peer must be rejected before any admission,
	// budget reservation or transport call can occur.
	s := &ControlledSchedulingService{}
	d, err := s.beginDispatch(ctx, 2, 10)
	require.ErrorIs(t, err, scheduling.ErrControlBlocked)
	require.Nil(t, d)
}

// Receiving response headers is not a semantic event and cannot spend the
// fallback window which the request already reserved at actual dispatch.
func TestIndependentReviewHeadersRetainReservedFallbackWindow(t *testing.T) {
	started := time.Now()
	policy := scheduling.NormalizePolicy(scheduling.Policy{Enabled: true})
	policy.Retry.SwitchMarginMS = 0
	profile := scheduling.LatencyProfile{HealthThresholdMS: 350, RecoveryThresholdMS: 250, AttemptTimeoutMS: 900, TotalBudgetMS: 1000, MinAttemptWindowMS: 350}
	ledger := scheduling.NewAttemptLedger(policy.Retry, profile, started, time.Time{})
	require.NoError(t, ledger.BeginAttempt(1, 0, started, true))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	d := &controlledDispatch{request: &ControlledRequest{Policy: policy, Profile: profile, Ledger: ledger}, ctx: ctx, cancel: cancel, started: started, semanticObservable: true, viableFallback: true}
	d.mu.Lock()
	d.startFirstOutputTimerLocked()
	d.mu.Unlock()
	t.Cleanup(func() {
		d.mu.Lock()
		if d.timer != nil {
			d.timer.Stop()
		}
		d.mu.Unlock()
	})
	time.Sleep(400 * time.Millisecond)
	if ctx.Err() != nil {
		t.Skip("host scheduling delay crossed original deadline before header simulation")
	}
	// roundTrip repeats this operation when successful SSE headers arrive.
	d.mu.Lock()
	d.startFirstOutputTimerLocked()
	d.mu.Unlock()
	deadline := time.NewTimer(time.Until(started.Add(750 * time.Millisecond)))
	defer deadline.Stop()
	select {
	case <-ctx.Done():
	case <-deadline.C:
		t.Fatal("headers extended the original 650ms attempt deadline; less than the reserved 350ms remains for fallback")
	}
}

// An overall deadline can expire while waiting for headers. It must not be
// replaced with the remaining per-attempt timeout during protocol detection.
func TestIndependentReviewExpiredBudgetNeverRestartsAttemptClock(t *testing.T) {
	policy := scheduling.NormalizePolicy(scheduling.Policy{Enabled: true})
	profile := scheduling.LatencyProfile{HealthThresholdMS: 200, RecoveryThresholdMS: 100, AttemptTimeoutMS: 900, TotalBudgetMS: 1000, MinAttemptWindowMS: 200}
	started := time.Now()
	ledger := scheduling.NewAttemptLedger(policy.Retry, profile, started.Add(-2*time.Second), time.Time{})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	d := &controlledDispatch{request: &ControlledRequest{Policy: policy, Profile: profile, Ledger: ledger}, ctx: ctx, cancel: cancel, started: started, semanticObservable: true}
	d.mu.Lock()
	d.startFirstOutputTimerLocked()
	d.mu.Unlock()
	select {
	case <-ctx.Done():
	case <-time.After(200 * time.Millisecond):
		t.Fatal("expired logical deadline was restarted using the per-attempt timeout")
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	require.True(t, d.clipped, "overall budget exhaustion cannot damage upstream health")
}
