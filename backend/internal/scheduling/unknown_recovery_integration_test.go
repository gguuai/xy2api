package scheduling

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func unknownRecoveryStore(t *testing.T) (*PostgresStore, *sql.DB) {
	t.Helper()
	s, db := isolatedControlStore(t)
	var exists bool
	require.NoError(t, db.QueryRow("SELECT EXISTS(SELECT 1 FROM information_schema.columns WHERE table_schema=current_schema() AND table_name='scheduling_attempts' AND column_name='unknown_hold_until')").Scan(&exists))
	if !exists {
		paths, err := filepath.Glob("../../migrations/264_*.sql")
		require.NoError(t, err)
		require.Len(t, paths, 1, "the recovery contract requires its actual additive migration")
		migration, err := os.ReadFile(paths[0])
		require.NoError(t, err)
		_, err = db.Exec(string(migration))
		require.NoError(t, err)
	}
	return s, db
}

func requireUnknownAuditPreserved(t *testing.T, db *sql.DB, ticket string) {
	t.Helper()
	var state string
	var usagePending, terminalIntent, unsettled bool
	require.NoError(t, db.QueryRow("SELECT state,usage_pending,COALESCE((metrics->>'terminal_intent')::boolean,FALSE),settled_at IS NULL FROM scheduling_attempts WHERE ticket_id=$1", ticket).Scan(&state, &usagePending, &terminalIntent, &unsettled))
	require.Equal(t, "unknown", state)
	require.True(t, usagePending, "bounded local occupancy must not acknowledge unknown usage")
	require.False(t, terminalIntent, "local cancellation is not evidence of an upstream terminal")
	require.True(t, unsettled)
}

func TestUnknownRecoveryCapacityHoldIsBounded(t *testing.T) {
	s, db := unknownRecoveryStore(t)
	ctx := context.Background()
	r := testDispatch(1, "")
	r.HardConcurrency = 1
	ticket, err := s.BeginDispatch(ctx, r)
	require.NoError(t, err)
	before := time.Now()
	require.NoError(t, s.MarkAttemptUnknown(ctx, ticket.TicketID))
	var hold, lease time.Time
	require.NoError(t, db.QueryRow("SELECT unknown_hold_until,lease_until FROM scheduling_attempts WHERE ticket_id=$1", ticket.TicketID).Scan(&hold, &lease))
	require.True(t, hold.After(before), "an unknown attempt must retain the short occupancy guard")
	require.False(t, hold.After(lease), "the unknown guard cannot extend the previous live lease")
	require.False(t, hold.After(before.Add(31*time.Second)), "local cancellation must bound occupancy to thirty seconds")
	counts, err := s.AccountActiveCounts(ctx, []int64{1})
	require.NoError(t, err)
	require.Equal(t, 1, counts[1])
	r.TicketID = NewDispatchID()
	_, err = s.BeginDispatch(ctx, r)
	require.ErrorIs(t, err, ErrCapacity)

	_, err = db.Exec("UPDATE scheduling_attempts SET unknown_hold_until=NOW()-INTERVAL '1 second' WHERE ticket_id=$1", ticket.TicketID)
	require.NoError(t, err)
	counts, err = s.AccountActiveCounts(ctx, []int64{1})
	require.NoError(t, err)
	require.Zero(t, counts[1])
	r.TicketID = NewDispatchID()
	next, err := s.BeginDispatch(ctx, r)
	require.NoError(t, err)
	require.NotEqual(t, ticket.TicketID, next.TicketID)
	requireUnknownAuditPreserved(t, db, ticket.TicketID)
	t.Log("unknown hold: occupancy blocks before expiry; later request admitted after expiry; unknown/usage audit remains unresolved")
}

func TestUnknownRecoveryExpiredDispatchDoesNotCreateFreshHold(t *testing.T) {
	s, db := unknownRecoveryStore(t)
	ctx := context.Background()
	r := testDispatch(1, "")
	r.HardConcurrency = 1
	ticket, err := s.BeginDispatch(ctx, r)
	require.NoError(t, err)
	_, err = db.Exec("UPDATE scheduling_attempts SET lease_until=NOW()-INTERVAL '1 minute' WHERE ticket_id=$1", ticket.TicketID)
	require.NoError(t, err)
	require.ErrorIs(t, s.RenewAttempt(ctx, ticket.TicketID, time.Minute), ErrAttemptIdentity, "an expired holder cannot renew itself")
	n, err := s.ReconcileExpired(ctx)
	require.NoError(t, err)
	require.EqualValues(t, 1, n)
	var hold, lease time.Time
	require.NoError(t, db.QueryRow("SELECT unknown_hold_until,lease_until FROM scheduling_attempts WHERE ticket_id=$1", ticket.TicketID).Scan(&hold, &lease))
	require.True(t, hold.Equal(lease), "crash reconciliation must use the already expired lease")
	require.True(t, hold.Before(time.Now()))
	require.NoError(t, s.MarkAttemptUnknown(ctx, ticket.TicketID))
	var repeated time.Time
	require.NoError(t, db.QueryRow("SELECT unknown_hold_until FROM scheduling_attempts WHERE ticket_id=$1", ticket.TicketID).Scan(&repeated))
	require.True(t, hold.Equal(repeated), "reconciliation or repeated cleanup cannot renew unknown occupancy")
	r.TicketID = NewDispatchID()
	_, err = s.BeginDispatch(ctx, r)
	require.NoError(t, err)
	requireUnknownAuditPreserved(t, db, ticket.TicketID)
	t.Log("expired live lease: reconciled to unresolved unknown without a fresh hold; later request admitted")
}

func TestUnknownRecoveryProbeExpiresAndRemainsSingleFlight(t *testing.T) {
	s, db := unknownRecoveryStore(t)
	ctx := context.Background()
	for _, id := range []int64{1, 3} {
		_, err := s.PutFailureDomains(ctx, AccountFailureDomains{AccountID: id, QuotaPoolID: "recovery-test-pool"}, 9)
		require.NoError(t, err)
	}
	failed := domainTicket(t, s, 1)
	decision := ClassifyFailure(FailureEvidence{Trusted: true, Status: 429, SharedKind: "quota_pool", SharedPool: "recovery-test-pool", ReplaySafe: true}, *failed.Failure, time.Now())
	require.NoError(t, s.SettleAttempt(ctx, failed.TicketID, "upstream_error", false))
	require.NoError(t, s.ApplyFailureFeedback(ctx, failed.TicketID, decision, false))
	_, err := db.Exec("UPDATE scheduling_failure_gates SET ready_after=NOW()-INTERVAL '1 second' WHERE gate_key=$1", decision.Key)
	require.NoError(t, err)
	unknownProbe := domainTicket(t, s, 1)
	require.Contains(t, unknownProbe.Failure.ProbeVersions, decision.Key)
	require.NoError(t, s.MarkAttemptUnknown(ctx, unknownProbe.TicketID))
	peer, err := s.FreezeFailureAdmission(ctx, 1, "model-a")
	require.NoError(t, err)
	r := testDispatch(1, "")
	r.Failure = &peer
	_, err = s.BeginDispatch(ctx, r)
	require.ErrorIs(t, err, ErrFailureDomainBlocked)

	_, err = db.Exec("UPDATE scheduling_attempts SET unknown_hold_until=NOW()-INTERVAL '1 second' WHERE ticket_id=$1", unknownProbe.TicketID)
	require.NoError(t, err)
	state, err := s.InspectFailureDomains(ctx, 1, "model-a")
	require.NoError(t, err)
	require.True(t, state.Eligible, "an expired unknown probe must not permanently block recovery")
	start := make(chan struct{})
	type result struct {
		ticket DispatchTicket
		err    error
	}
	results := make(chan result, 2)
	var wg sync.WaitGroup
	for _, id := range []int64{1, 1} {
		admission, freezeErr := s.FreezeFailureAdmission(ctx, id, "model-a")
		require.NoError(t, freezeErr)
		wg.Add(1)
		go func(a FailureAdmission) {
			defer wg.Done()
			<-start
			request := testDispatch(a.AccountID, "")
			request.Failure = &a
			ticket, beginErr := s.BeginDispatch(ctx, request)
			results <- result{ticket: ticket, err: beginErr}
		}(admission)
	}
	close(start)
	wg.Wait()
	close(results)
	admitted, blocked := 0, 0
	var winner DispatchTicket
	for value := range results {
		switch {
		case value.err == nil:
			admitted++
			winner = value.ticket
		case errors.Is(value.err, ErrFailureDomainBlocked):
			blocked++
		default:
			require.NoError(t, value.err)
		}
	}
	require.Equal(t, 1, admitted)
	require.Equal(t, 1, blocked)
	require.Contains(t, winner.Failure.ProbeVersions, decision.Key)
	requireUnknownAuditPreserved(t, db, unknownProbe.TicketID)
	state, err = s.InspectFailureDomains(ctx, 1, "model-a")
	require.NoError(t, err)
	require.False(t, state.Eligible, "the admitted recovery probe must exclude simultaneous peers")
	require.NoError(t, s.SettleAttempt(ctx, winner.TicketID, "completed", false))
	require.NoError(t, s.ApplyFailureFeedback(ctx, winner.TicketID, FailureDecision{Effect: "none"}, true))
	state, err = s.InspectFailureDomains(ctx, 1, "model-a")
	require.NoError(t, err)
	require.True(t, state.Eligible)
	requireUnknownAuditPreserved(t, db, unknownProbe.TicketID)
	t.Log("recovery: held unknown probe blocks; expired hold admits exactly one concurrent probe; success restores service without inventing the old terminal")
}

func TestUnknownRecoveryDoesNotDeclareManualDrainComplete(t *testing.T) {
	s, db := unknownRecoveryStore(t)
	ctx := context.Background()
	ticket, err := s.BeginDispatch(ctx, testDispatch(1, ""))
	require.NoError(t, err)
	_, err = s.Control(ctx, ControlCommand{AccountID: 1, Action: "pause"})
	require.NoError(t, err)
	_, err = db.Exec("UPDATE accounts SET schedulable=FALSE WHERE id=1")
	require.NoError(t, err)
	require.NoError(t, s.MarkAttemptUnknown(ctx, ticket.TicketID))
	_, err = db.Exec("UPDATE scheduling_attempts SET unknown_hold_until=NOW()-INTERVAL '1 second' WHERE ticket_id=$1", ticket.TicketID)
	require.NoError(t, err)
	control, err := s.GetControl(ctx, 1, ScopeAccount)
	require.NoError(t, err)
	require.EqualValues(t, 1, control.UnknownAttempts)
	require.Equal(t, ControlUncertain, control.State)
	_, err = s.BeginDispatch(ctx, testDispatch(1, ""))
	require.ErrorIs(t, err, ErrControlBlocked, "expiry cannot undo an administrator pause")
	requireUnknownAuditPreserved(t, db, ticket.TicketID)
	t.Log("manual pause stays blocked and reports unresolved unknown after local capacity hold expires")
}

// A finished local probe can leave a delayed terminal/feedback intent. Releasing
// its bounded hold must not let that old result recover a replacement probe.
func TestUnknownRecoveryLateSuccessCannotClearReplacementProbe(t *testing.T) {
	unknownRecoveryLateFeedbackCannotClearReplacement(t, true)
}
func TestUnknownRecoveryLateFailureCannotClearReplacementProbe(t *testing.T) {
	unknownRecoveryLateFeedbackCannotClearReplacement(t, false)
}
func unknownRecoveryLateFeedbackCannotClearReplacement(t *testing.T, completed bool) {
	t.Helper()
	s, db := isolatedControlStore(t)
	ctx := context.Background()
	for _, id := range []int64{1, 3} {
		_, err := s.PutFailureDomains(ctx, AccountFailureDomains{AccountID: id, QuotaPoolID: "late-probe-pool"}, 1)
		require.NoError(t, err)
	}
	oldAdmission, err := s.FreezeFailureAdmission(ctx, 1, "m")
	require.NoError(t, err)
	gateKey := oldAdmission.ModelKey()
	_, err = db.Exec("INSERT INTO scheduling_failure_gates(gate_key,scope,reason,version,ready_after) VALUES($1,'account_model','recovery',7,NOW()-INTERVAL '1 second')", gateKey)
	require.NoError(t, err)
	oldRequest := testDispatch(1, "")
	oldRequest.Failure = &oldAdmission
	old, err := s.BeginDispatch(ctx, oldRequest)
	require.NoError(t, err)
	require.NoError(t, s.MarkAttemptUnknown(ctx, old.TicketID))
	_, err = db.Exec("UPDATE scheduling_attempts SET unknown_hold_until=NOW()-INTERVAL '1 second' WHERE ticket_id=$1", old.TicketID)
	require.NoError(t, err)
	replacementAdmission, err := s.FreezeFailureAdmission(ctx, 1, "m")
	require.NoError(t, err)
	replacementRequest := testDispatch(1, "")
	replacementRequest.Failure = &replacementAdmission
	replacement, err := s.BeginDispatch(ctx, replacementRequest)
	require.NoError(t, err)
	// Deliver a real observed old terminal only after the replacement owns the
	// recovery probe. The old audit may settle; the replacement must stay fenced.
	outcome := "completed"
	if !completed {
		outcome = "failed"
	}
	require.NoError(t, s.SettleAttempt(ctx, old.TicketID, outcome, true))
	require.NoError(t, s.ApplyFailureFeedback(ctx, old.TicketID, FailureDecision{Effect: "none"}, completed))
	anotherAdmission, err := s.FreezeFailureAdmission(ctx, 1, "m")
	require.NoError(t, err)
	anotherRequest := testDispatch(1, "")
	anotherRequest.Failure = &anotherAdmission
	another, admitErr := s.BeginDispatch(ctx, anotherRequest)
	t.Logf("late old result completed=%t: old_probe_version=%d replacement_probe_version=%d later_admitted=%t later_error=%v", completed, old.Failure.ProbeVersions[gateKey], replacement.Failure.ProbeVersions[gateKey], admitErr == nil, admitErr)
	if admitErr == nil {
		require.NoError(t, s.SettleAttempt(ctx, another.TicketID, "not_sent", false))
	}
	require.ErrorIs(t, admitErr, ErrFailureDomainBlocked, "an old terminal cannot open a gate held by a newer recovery probe")
	var state string
	require.NoError(t, db.QueryRow("SELECT state FROM scheduling_attempts WHERE ticket_id=$1", replacement.TicketID).Scan(&state))
	require.Equal(t, "dispatched", state)
	require.NoError(t, s.SettleAttempt(ctx, replacement.TicketID, "completed", true))
	require.NoError(t, s.ApplyFailureFeedback(ctx, replacement.TicketID, FailureDecision{Effect: "none"}, true))
	recovered, err := s.InspectFailureDomains(ctx, 1, "m")
	require.NoError(t, err)
	require.True(t, recovered.Eligible, "the current probe still owns successful recovery")
}
