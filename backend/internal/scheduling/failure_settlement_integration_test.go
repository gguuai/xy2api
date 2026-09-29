package scheduling

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestFailureProbeSettlementFailureRollsBackFeedbackAudit(t *testing.T) {
	s, db := isolatedControlStore(t)
	ctx := context.Background()
	for _, id := range []int64{1, 3} {
		_, err := s.PutFailureDomains(ctx, AccountFailureDomains{AccountID: id, QuotaPoolID: "org-a"}, 9)
		require.NoError(t, err)
	}
	failed := domainTicket(t, s, 1)
	decision := ClassifyFailure(FailureEvidence{Trusted: true, Status: 429, SharedKind: "quota_pool", SharedPool: "org-a", ReplaySafe: true}, *failed.Failure, time.Now())
	require.NoError(t, s.SettleAttempt(ctx, failed.TicketID, "upstream_error", false))
	require.NoError(t, s.ApplyFailureFeedback(ctx, failed.TicketID, decision, false))
	_, err := db.ExecContext(ctx, "UPDATE scheduling_failure_gates SET ready_after=NOW()-INTERVAL '1 second' WHERE gate_key=$1", decision.Key)
	require.NoError(t, err)
	probe := domainTicket(t, s, 1)
	require.Contains(t, probe.Failure.ProbeVersions, decision.Key)
	completed := FailureDecision{Effect: "none"}
	require.NoError(t, s.RecordTerminalFailureIntent(ctx, probe.TicketID, "completed", "remote_terminal", false, completed, true))
	var terminalIntent, failureCompleted bool
	var terminalOutcome string
	require.NoError(t, db.QueryRowContext(ctx, `SELECT (metrics->>'terminal_intent')::boolean,
		metrics->>'terminal_outcome', (metrics->'failure_intent'->>'completed')::boolean
		FROM scheduling_attempts WHERE ticket_id=$1`, probe.TicketID).Scan(&terminalIntent, &terminalOutcome, &failureCompleted))
	require.True(t, terminalIntent)
	require.Equal(t, "completed", terminalOutcome)
	require.True(t, failureCompleted)
	var gateVersion int64
	var gateReady time.Time
	var gateReason string
	require.NoError(t, db.QueryRowContext(ctx, "SELECT version,ready_after,reason FROM scheduling_failure_gates WHERE gate_key=$1", decision.Key).Scan(&gateVersion, &gateReady, &gateReason))

	// Sequence increments survive transaction rollback, so only the first real
	// PostgreSQL settlement fails and the durable intents remain replayable.
	_, err = db.ExecContext(ctx, `
		CREATE SEQUENCE failure_probe_settlement_fault;
		CREATE FUNCTION fail_first_probe_settlement() RETURNS trigger AS $$
		BEGIN
			IF NEW.state='settled' AND OLD.state<>'settled'
			   AND nextval('failure_probe_settlement_fault')=1 THEN
				RAISE EXCEPTION 'injected first probe settlement failure';
			END IF;
			RETURN NEW;
		END; $$ LANGUAGE plpgsql;
		CREATE TRIGGER failure_probe_settlement_fault BEFORE UPDATE ON scheduling_attempts
		FOR EACH ROW EXECUTE FUNCTION fail_first_probe_settlement();
	`)
	require.NoError(t, err)
	err = s.SettleAttempt(ctx, probe.TicketID, "completed", false)
	require.ErrorContains(t, err, "injected first probe settlement failure")
	require.ErrorIs(t, s.ApplyFailureFeedback(ctx, probe.TicketID, completed, true), ErrAttemptIdentity)

	peer, err := s.FreezeFailureAdmission(ctx, 1, "model-a")
	require.NoError(t, err)
	peerRequest := testDispatch(1, "")
	peerRequest.Failure = &peer
	_, err = s.BeginDispatch(ctx, peerRequest)
	require.ErrorIs(t, err, ErrFailureDomainBlocked)
	var state string
	require.NoError(t, db.QueryRowContext(ctx, "SELECT state FROM scheduling_attempts WHERE ticket_id=$1", probe.TicketID).Scan(&state))
	require.Equal(t, "dispatched", state)
	var audits int
	require.NoError(t, db.QueryRowContext(ctx, "SELECT count(*) FROM scheduling_failure_audit WHERE ticket_id=$1 AND action='failure_feedback'", probe.TicketID).Scan(&audits))
	require.Zero(t, audits, "the rejected success feedback must roll back its audit insert")
	t.Log("settlement trigger failed: attempt=dispatched, success feedback rejected, feedback audits=0, peer dispatch=domain blocked")

	require.NoError(t, s.MarkAttemptUnknown(ctx, probe.TicketID))
	restarted := NewPostgresStore(db)
	n, err := restarted.ReconcileFailureIntents(ctx)
	require.ErrorIs(t, err, ErrAttemptIdentity)
	require.Zero(t, n)
	require.NoError(t, db.QueryRowContext(ctx, "SELECT count(*) FROM scheduling_failure_audit WHERE ticket_id=$1 AND action='failure_feedback'", probe.TicketID).Scan(&audits))
	require.Zero(t, audits, "an early reconciliation must leave the failure intent pending")
	var currentVersion int64
	var currentReady time.Time
	var currentReason string
	require.NoError(t, db.QueryRowContext(ctx, "SELECT version,ready_after,reason FROM scheduling_failure_gates WHERE gate_key=$1", decision.Key).Scan(&currentVersion, &currentReady, &currentReason))
	require.Equal(t, gateVersion, currentVersion)
	require.Equal(t, gateReady, currentReady)
	require.Equal(t, gateReason, currentReason)
	_, err = restarted.BeginDispatch(ctx, peerRequest)
	require.ErrorIs(t, err, ErrFailureDomainBlocked)
	t.Log("early reconciliation: attempt=unknown, unchanged cooldown/version, feedback audits=0, peer dispatch=domain blocked")

	n, err = restarted.ReconcileTerminalIntents(ctx)
	require.NoError(t, err)
	require.EqualValues(t, 1, n)
	require.NoError(t, db.QueryRowContext(ctx, "SELECT state,outcome FROM scheduling_attempts WHERE ticket_id=$1", probe.TicketID).Scan(&state, &terminalOutcome))
	require.Equal(t, "settled", state)
	require.Equal(t, "completed", terminalOutcome)
	// The earlier unsettled-probe failure retains its bounded backoff. Advance
	// only this test clock in PG before simulating the next reconciliation.
	_, err = db.ExecContext(ctx, "UPDATE scheduling_attempts SET failure_feedback_next_retry_at=NOW() WHERE ticket_id=$1", probe.TicketID)
	require.NoError(t, err)
	n, err = restarted.ReconcileFailureIntents(ctx)
	require.NoError(t, err)
	require.EqualValues(t, 1, n, "no early audit may suppress compensation")
	var readyCleared bool
	require.NoError(t, db.QueryRowContext(ctx, "SELECT ready_after IS NULL,version,reason FROM scheduling_failure_gates WHERE gate_key=$1", decision.Key).Scan(&readyCleared, &currentVersion, &currentReason))
	require.True(t, readyCleared)
	require.Equal(t, gateVersion+1, currentVersion)
	require.Equal(t, "recovery_confirmed", currentReason)
	peerTicket, err := restarted.BeginDispatch(ctx, peerRequest)
	require.NoError(t, err)
	require.Empty(t, peerTicket.Failure.ProbeVersions)
	n, err = restarted.ReconcileFailureIntents(ctx)
	require.NoError(t, err)
	require.Zero(t, n)
	require.NoError(t, db.QueryRowContext(ctx, "SELECT count(*) FROM scheduling_failure_audit WHERE ticket_id=$1 AND action='failure_feedback'", probe.TicketID).Scan(&audits))
	require.Equal(t, 1, audits)
	t.Log("compensation: attempt=settled/completed, recovery confirmed, feedback audits=1, peer dispatch=admitted, replay=0")
}

func TestFailureUnknownIndependent429ClosesAccountDomainBeforeSettlement(t *testing.T) {
	s, db := isolatedControlStore(t)
	ctx := context.Background()
	for _, id := range []int64{1, 3} {
		_, err := s.PutFailureDomains(ctx, AccountFailureDomains{AccountID: id, QuotaPoolID: "org-a"}, 9)
		require.NoError(t, err)
	}
	ticket := domainTicket(t, s, 1)
	require.Empty(t, ticket.Failure.ProbeVersions)
	require.NoError(t, s.MarkAttemptUnknown(ctx, ticket.TicketID))
	decision := ClassifyFailure(FailureEvidence{Trusted: true, Status: 429, SharedKind: "quota_pool", SharedPool: "org-a", ReplaySafe: true, ClientCancelled: true}, *ticket.Failure, time.Now())
	require.Equal(t, "account_model", decision.Scope)
	require.Equal(t, "cooldown", decision.Effect)
	require.Equal(t, "stop", decision.Retry)
	require.NoError(t, s.RecordFailureIntent(ctx, ticket.TicketID, decision, false))
	n, err := s.ReconcileFailureIntents(ctx)
	require.NoError(t, err)
	require.EqualValues(t, 1, n)
	var state string
	var terminalIntent bool
	require.NoError(t, db.QueryRowContext(ctx, "SELECT state,metrics ? 'terminal_intent' FROM scheduling_attempts WHERE ticket_id=$1", ticket.TicketID).Scan(&state, &terminalIntent))
	require.Equal(t, "unknown", state)
	require.False(t, terminalIntent)
	var gateClosed bool
	require.NoError(t, db.QueryRowContext(ctx, "SELECT ready_after>NOW() FROM scheduling_failure_gates WHERE gate_key=$1", decision.Key).Scan(&gateClosed))
	require.True(t, gateClosed)
	peer, err := s.FreezeFailureAdmission(ctx, 1, "model-a")
	require.NoError(t, err)
	peerRequest := testDispatch(1, "")
	peerRequest.Failure = &peer
	_, err = s.BeginDispatch(ctx, peerRequest)
	require.ErrorIs(t, err, ErrFailureDomainBlocked)
	var audits int
	require.NoError(t, db.QueryRowContext(ctx, "SELECT count(*) FROM scheduling_failure_audit WHERE ticket_id=$1 AND action='failure_feedback' AND evidence->>'completed'='false'", ticket.TicketID).Scan(&audits))
	require.Equal(t, 1, audits)
	t.Log("independent trusted 429: attempt=unknown, terminal intent=absent, account cooldown=closed, failure audits=1, peer dispatch=domain blocked")
}
