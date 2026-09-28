package scheduling

import (
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func queueFailureFixture(t *testing.T, s *PostgresStore, id int64) (DispatchTicket, FailureDecision) {
	t.Helper()
	ticket := domainTicket(t, s, id)
	decision := ClassifyFailure(FailureEvidence{Trusted: true, Status: 429, ReplaySafe: true}, *ticket.Failure, time.Now())
	require.NoError(t, s.SettleAttempt(context.Background(), ticket.TicketID, "upstream_error", false))
	require.NoError(t, s.RecordFailureIntent(context.Background(), ticket.TicketID, decision, false))
	return ticket, decision
}

func requireFeedbackDone(t *testing.T, db *sql.DB, ticket string, want bool) {
	t.Helper()
	var done bool
	var next sql.NullTime
	var count int
	require.NoError(t, db.QueryRow("SELECT failure_feedback_done,failure_feedback_next_retry_at,failure_feedback_retry_count FROM scheduling_attempts WHERE ticket_id=$1", ticket).Scan(&done, &next, &count))
	require.Equal(t, want, done)
	if want {
		require.False(t, next.Valid)
		require.Zero(t, count)
	} else {
		require.True(t, next.Valid)
	}
}

func TestFailureFeedbackClaimCrashAndBackoff(t *testing.T) {
	s, db := isolatedControlStore(t)
	ctx := context.Background()
	ticket, _ := queueFailureFixture(t, s, 1)
	// Claim without applying: models a worker exiting after its committed claim.
	for retry, expected := range []int{5, 10, 20, 40, 80, 160, 300, 300} {
		rows, err := db.QueryContext(ctx, claimFailureIntentsSQL)
		require.NoError(t, err)
		require.True(t, rows.Next())
		var id string
		var raw []byte
		require.NoError(t, rows.Scan(&id, &raw))
		require.Equal(t, ticket.TicketID, id)
		require.False(t, rows.Next())
		require.NoError(t, rows.Err())
		require.NoError(t, rows.Close())
		var wait float64
		require.NoError(t, db.QueryRow("SELECT EXTRACT(EPOCH FROM failure_feedback_next_retry_at-NOW()) FROM scheduling_attempts WHERE ticket_id=$1", ticket.TicketID).Scan(&wait))
		require.InDelta(t, expected, wait, 2)
		n, err := s.ReconcileFailureIntents(ctx)
		require.NoError(t, err)
		require.Zero(t, n, "a fresh claim must not be stolen")
		requireFeedbackDone(t, db, ticket.TicketID, false)
		_, err = db.Exec("UPDATE scheduling_attempts SET failure_feedback_next_retry_at=NOW()-INTERVAL '1 second' WHERE ticket_id=$1", ticket.TicketID)
		require.NoError(t, err)
		t.Logf("crashed_claim=%d retry_delay_seconds=%d pending_retained=true", retry+1, expected)
	}
	n, err := s.ReconcileFailureIntents(ctx)
	require.NoError(t, err)
	require.EqualValues(t, 1, n)
	state, err := s.InspectFailureDomains(ctx, 1, "model-a")
	require.NoError(t, err)
	require.False(t, state.Eligible)
	requireFeedbackDone(t, db, ticket.TicketID, true)
}

func TestFailureFeedbackTransientBatchDoesNotStarveAndEventuallyRetries(t *testing.T) {
	s, db := isolatedControlStore(t)
	ctx := context.Background()
	for i := 0; i < 100; i++ {
		queueFailureFixture(t, s, 1)
	}
	queueFailureFixture(t, s, 3)
	_, err := db.Exec(`CREATE FUNCTION reject_feedback_temporarily() RETURNS trigger LANGUAGE plpgsql AS $$
	BEGIN IF NEW.account_id=1 AND NEW.action='failure_feedback' THEN RAISE EXCEPTION 'temporary feedback write failure'; END IF; RETURN NEW; END $$;
	CREATE TRIGGER reject_feedback_temporarily BEFORE INSERT ON scheduling_failure_audit FOR EACH ROW EXECUTE FUNCTION reject_feedback_temporarily();`)
	require.NoError(t, err)
	n, err := s.ReconcileFailureIntents(ctx)
	require.ErrorContains(t, err, "temporary feedback write failure")
	require.Zero(t, n)
	n, err = s.ReconcileFailureIntents(ctx)
	require.NoError(t, err)
	require.EqualValues(t, 1, n, "backed-off failures must yield to the later live gate")
	state, err := s.InspectFailureDomains(ctx, 3, "model-a")
	require.NoError(t, err)
	require.False(t, state.Eligible)
	_, err = db.Exec("DROP TRIGGER reject_feedback_temporarily ON scheduling_failure_audit; UPDATE scheduling_attempts SET failure_feedback_next_retry_at=NOW() WHERE NOT failure_feedback_done")
	require.NoError(t, err)
	n, err = s.ReconcileFailureIntents(ctx)
	require.NoError(t, err)
	require.EqualValues(t, 100, n)
	n, err = s.ReconcileFailureIntents(ctx)
	require.NoError(t, err)
	require.Zero(t, n)
	t.Log("100 transient failures yielded to live gate, then all 100 recovered after backoff")
}

func TestFailureFeedbackSlowTicketDoesNotConsumeBatchDeadline(t *testing.T) {
	s, db := isolatedControlStore(t)
	slow, _ := queueFailureFixture(t, s, 1)
	live, decision := queueFailureFixture(t, s, 3)
	_, err := db.Exec(`CREATE FUNCTION delay_one_feedback() RETURNS trigger LANGUAGE plpgsql AS $$
	BEGIN IF NEW.ticket_id = '` + slow.TicketID + `' AND NEW.action = 'failure_feedback' THEN
		PERFORM pg_sleep(3);
	END IF; RETURN NEW; END $$;
	CREATE TRIGGER delay_one_feedback BEFORE INSERT ON scheduling_failure_audit
	FOR EACH ROW EXECUTE FUNCTION delay_one_feedback();`)
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	n, err := s.ReconcileFailureIntents(ctx)
	require.Error(t, err)
	require.EqualValues(t, 1, n, "a slow feedback insert must not consume the live ticket's deadline")
	requireFeedbackDone(t, db, slow.TicketID, false)
	requireFeedbackDone(t, db, live.TicketID, true)
	state, err := s.InspectFailureDomains(context.Background(), 3, "model-a")
	require.NoError(t, err)
	require.False(t, state.Eligible)
	var version int
	require.NoError(t, db.QueryRow("SELECT version FROM scheduling_failure_gates WHERE gate_key=$1", decision.Key).Scan(&version))
	require.Equal(t, 1, version)
}

func TestFailureFeedbackConcurrentReceiptAndAtomicAcknowledgement(t *testing.T) {
	s, db := isolatedControlStore(t)
	ticket, decision := queueFailureFixture(t, s, 1)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	start := make(chan struct{})
	errs := make(chan error, 20)
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			if i%2 == 0 {
				errs <- s.ApplyFailureFeedback(ctx, ticket.TicketID, decision, false)
			} else {
				_, err := NewPostgresStore(db).ReconcileFailureIntents(ctx)
				errs <- err
			}
		}(i)
	}
	close(start)
	wg.Wait()
	close(errs)
	for err := range errs {
		require.NoError(t, err)
	}
	var audits, version int
	require.NoError(t, db.QueryRow("SELECT count(*) FROM scheduling_failure_audit WHERE ticket_id=$1 AND action='failure_feedback'", ticket.TicketID).Scan(&audits))
	require.NoError(t, db.QueryRow("SELECT version FROM scheduling_failure_gates WHERE gate_key=$1", decision.Key).Scan(&version))
	require.Equal(t, 1, audits)
	require.Equal(t, 1, version)
	requireFeedbackDone(t, db, ticket.TicketID, true)
	// A late intent/replay must not recreate pending work or change its decision.
	require.NoError(t, s.RecordFailureIntent(ctx, ticket.TicketID, FailureDecision{Effect: "none"}, true))
	requireFeedbackDone(t, db, ticket.TicketID, true)
	n, err := s.ReconcileFailureIntents(ctx)
	require.NoError(t, err)
	require.Zero(t, n)
}

func TestFailureFeedbackAcknowledgementRollbackAndLateIntent(t *testing.T) {
	s, db := isolatedControlStore(t)
	ctx := context.Background()
	ticket, decision := queueFailureFixture(t, s, 1)
	_, err := db.Exec(`CREATE FUNCTION reject_feedback_ack() RETURNS trigger LANGUAGE plpgsql AS $$
	BEGIN IF NEW.failure_feedback_done AND NOT OLD.failure_feedback_done THEN RAISE EXCEPTION 'injected feedback acknowledgement failure'; END IF; RETURN NEW; END $$;
	CREATE TRIGGER reject_feedback_ack BEFORE UPDATE ON scheduling_attempts FOR EACH ROW EXECUTE FUNCTION reject_feedback_ack();`)
	require.NoError(t, err)
	require.ErrorContains(t, s.ApplyFailureFeedback(ctx, ticket.TicketID, decision, false), "injected feedback acknowledgement failure")
	var audits, version int
	require.NoError(t, db.QueryRow("SELECT count(*) FROM scheduling_failure_audit WHERE ticket_id=$1", ticket.TicketID).Scan(&audits))
	require.NoError(t, db.QueryRow("SELECT version FROM scheduling_failure_gates WHERE gate_key=$1", decision.Key).Scan(&version))
	require.Zero(t, audits)
	require.Zero(t, version)
	requireFeedbackDone(t, db, ticket.TicketID, false)
	_, err = db.Exec("DROP TRIGGER reject_feedback_ack ON scheduling_attempts")
	require.NoError(t, err)
	require.NoError(t, s.ApplyFailureFeedback(ctx, ticket.TicketID, decision, false))
	requireFeedbackDone(t, db, ticket.TicketID, true)

	late := domainTicket(t, s, 3)
	require.NoError(t, s.ApplyFailureFeedback(ctx, late.TicketID, FailureDecision{Effect: "none"}, false))
	require.NoError(t, s.RecordFailureIntent(ctx, late.TicketID, FailureDecision{Effect: "none"}, false))
	requireFeedbackDone(t, db, late.TicketID, true)
	require.NoError(t, s.RecordTerminalFailureIntent(ctx, late.TicketID, "completed", "remote_terminal", false, FailureDecision{Effect: "none"}, true))
	requireFeedbackDone(t, db, late.TicketID, true)
	require.ErrorIs(t, s.RecordFailureIntent(ctx, "missing-ticket", FailureDecision{}, false), ErrAttemptIdentity)
}

func TestFailureFeedbackMigrationBackfillAndCompletedHistoryIndex(t *testing.T) {
	s, db := isolatedControlStore(t)
	ctx := context.Background()
	audited := domainTicket(t, s, 1)
	pending := domainTicket(t, s, 3)
	late := domainTicket(t, s, 3)
	_, err := db.Exec(`DROP INDEX scheduling_attempts_failure_feedback_pending;
	ALTER TABLE scheduling_attempts DROP COLUMN failure_feedback_done,DROP COLUMN failure_feedback_next_retry_at,DROP COLUMN failure_feedback_retry_count;`)
	require.NoError(t, err)
	_, err = db.Exec(`UPDATE scheduling_attempts SET metrics='{"failure_intent":{"decision":{"effect":"none"},"completed":false}}'::jsonb WHERE ticket_id IN ($1,$2)`, audited.TicketID, pending.TicketID)
	require.NoError(t, err)
	_, err = db.Exec(`INSERT INTO scheduling_failure_audit(account_id,ticket_id,action,evidence) VALUES(1,$1,'failure_feedback','{}'),(3,$2,'failure_feedback','{}')`, audited.TicketID, late.TicketID)
	require.NoError(t, err)
	raw, err := os.ReadFile("../../migrations/261_scheduling_failure_feedback_queue.sql")
	require.NoError(t, err)
	_, err = db.Exec(string(raw))
	require.NoError(t, err)
	requireFeedbackDone(t, db, audited.TicketID, true)
	requireFeedbackDone(t, db, late.TicketID, true)
	requireFeedbackDone(t, db, pending.TicketID, false)
	n, err := s.ReconcileFailureIntents(ctx)
	require.NoError(t, err)
	require.EqualValues(t, 1, n)
	requireFeedbackDone(t, db, pending.TicketID, true)
	// Reapplying the migration does not revive acknowledged work.
	_, err = db.Exec(string(raw))
	require.NoError(t, err)
	n, err = s.ReconcileFailureIntents(ctx)
	require.NoError(t, err)
	require.Zero(t, n)
	_, err = db.Exec(`INSERT INTO scheduling_attempts(ticket_id,request_id,account_id,family_id,node_id,account_epoch,family_epoch,state,lease_until,metrics,failure_feedback_done)
	SELECT 'history-'||n,'req-'||n,1,1,'node',0,0,'settled',NOW(),'{"failure_intent":{"decision":{"effect":"none"},"completed":true}}'::jsonb,TRUE FROM generate_series(1,20000) n;
	INSERT INTO scheduling_failure_audit(account_id,ticket_id,action,evidence) SELECT 1,ticket_id,'failure_feedback','{}'::jsonb FROM scheduling_attempts WHERE ticket_id LIKE 'history-%';
	ANALYZE scheduling_attempts; ANALYZE scheduling_failure_audit;`)
	require.NoError(t, err)
	var plan []byte
	require.NoError(t, db.QueryRow("EXPLAIN (ANALYZE,BUFFERS,FORMAT JSON) "+claimFailureIntentsSQL).Scan(&plan))
	require.True(t, json.Valid(plan))
	require.Contains(t, string(plan), "scheduling_attempts_failure_feedback_pending")
	require.NotContains(t, string(plan), `"Seq Scan"`, "an empty pending set must not scan completed history")
	require.NotContains(t, string(plan), `"scheduling_failure_audit"`, "audit history is not part of queue selection")
	t.Log("completed_history=20000 pending=0 plan=" + strings.ReplaceAll(string(plan), "\n", ""))
}

func TestFailureFeedbackDeletedChildAndParentRemainIgnored(t *testing.T) {
	for _, deleted := range []string{"2", "1,2"} {
		t.Run(deleted, func(t *testing.T) {
			s, db := isolatedControlStore(t)
			ctx := context.Background()
			ticket, decision := queueFailureFixture(t, s, 2)
			_, err := db.Exec("UPDATE accounts SET deleted_at=NOW() WHERE id IN (" + deleted + ")")
			require.NoError(t, err)
			require.NoError(t, s.ApplyFailureFeedback(ctx, ticket.TicketID, decision, false))
			require.NoError(t, s.ApplyFailureFeedback(ctx, ticket.TicketID, decision, false))
			requireFeedbackDone(t, db, ticket.TicketID, true)
			var count int
			require.NoError(t, db.QueryRow("SELECT count(*) FROM scheduling_failure_audit WHERE ticket_id=$1 AND evidence->>'stale_identity'='true' AND evidence->>'identity_missing'='true'", ticket.TicketID).Scan(&count))
			require.Equal(t, 1, count)
			require.NoError(t, db.QueryRow("SELECT version FROM scheduling_failure_gates WHERE gate_key=$1", decision.Key).Scan(&count))
			require.Zero(t, count, "deleted identities cannot close current gates")
		})
	}
}
