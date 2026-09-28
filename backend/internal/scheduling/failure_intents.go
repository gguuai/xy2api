package scheduling

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"
)

type failureIntent struct {
	Decision  FailureDecision `json:"decision"`
	Completed bool            `json:"completed"`
}

// Reuses existing ticket metrics as a durable outbox, not a second ledger.
func (s *PostgresStore) RecordFailureIntent(ctx context.Context, ticket string, d FailureDecision, completed bool) error {
	if err := s.ready(); err != nil {
		return err
	}
	if ticket == "" {
		return ErrInvalidControl
	}
	raw, err := json.Marshal(failureIntent{d, completed})
	if err != nil {
		return err
	}
	result, err := s.db.ExecContext(ctx, "UPDATE scheduling_attempts SET metrics=metrics || jsonb_build_object('failure_intent',$2::jsonb),failure_feedback_next_retry_at=CASE WHEN failure_feedback_done THEN NULL ELSE NOW() END WHERE ticket_id=$1 AND NOT (metrics ? 'failure_intent')", ticket, string(raw))
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err != nil || n != 0 {
		return err
	}
	var exists bool
	if err = s.db.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM scheduling_attempts WHERE ticket_id=$1)", ticket).Scan(&exists); err != nil {
		return err
	}
	if !exists {
		return ErrAttemptIdentity
	}
	return nil
}

// Claim only the indexed pending set. Each claim commits before identity/gate
// locks are acquired, preserving admission's lock order. Claiming one at a time
// avoids putting unprocessed tickets into backoff when the worker deadline ends.
// A worker crash leaves a bounded retry deadline, never an indefinitely owned entry.
const claimFailureIntentsSQL = `WITH due AS (
	SELECT ticket_id FROM scheduling_attempts
	WHERE NOT failure_feedback_done AND failure_feedback_next_retry_at IS NOT NULL
		AND failure_feedback_next_retry_at<=NOW()
	ORDER BY failure_feedback_next_retry_at,ticket_id
	LIMIT 1 FOR UPDATE SKIP LOCKED
) UPDATE scheduling_attempts a SET
	failure_feedback_next_retry_at=NOW()+LEAST(300,5*(1<<LEAST(a.failure_feedback_retry_count,6)))*INTERVAL '1 second',
	failure_feedback_retry_count=LEAST(a.failure_feedback_retry_count+1,6)
FROM due WHERE a.ticket_id=due.ticket_id
RETURNING a.ticket_id,a.metrics->'failure_intent'`

func (s *PostgresStore) ReconcileFailureIntents(ctx context.Context) (int64, error) {
	if err := s.ready(); err != nil {
		return 0, err
	}
	var n int64
	var first error
	for claimed := 0; claimed < 100; claimed++ {
		if err := ctx.Err(); err != nil {
			if first != nil {
				return n, first
			}
			return n, err
		}
		var id string
		var raw []byte
		err := s.db.QueryRowContext(ctx, claimFailureIntentsSQL).Scan(&id, &raw)
		if errors.Is(err, sql.ErrNoRows) {
			break
		}
		if err != nil {
			return n, err
		}
		var intent failureIntent
		err = json.Unmarshal(raw, &intent)
		if err == nil {
			applyCtx, cancel := context.WithTimeout(ctx, time.Second)
			err = s.ApplyFailureFeedback(applyCtx, id, intent.Decision, intent.Completed)
			cancel()
		}
		if err != nil {
			if first == nil {
				first = err
			}
			continue
		}
		n++
	}
	return n, first
}

// Both facts live in the existing attempt row. A crash cannot leave a durable
// terminal settlement intent without its matching failure classification.
func (s *PostgresStore) RecordTerminalFailureIntent(ctx context.Context, ticket, outcome, certainty string, usagePending bool, d FailureDecision, completed bool) error {
	if err := s.ready(); err != nil {
		return err
	}
	if ticket == "" || outcome == "" || certainty == "" || len(outcome) > 256 || len(certainty) > 64 {
		return ErrInvalidControl
	}
	raw, err := json.Marshal(failureIntent{d, completed})
	if err != nil {
		return err
	}
	result, err := s.db.ExecContext(ctx, "UPDATE scheduling_attempts SET metrics=metrics || jsonb_build_object('terminal_intent',true,'terminal_outcome',$2::text,'terminal_certainty',$3::text,'terminal_usage_pending',$4::boolean,'failure_intent',COALESCE(metrics->'failure_intent',$5::jsonb)),failure_feedback_next_retry_at=CASE WHEN failure_feedback_done THEN NULL ELSE COALESCE(failure_feedback_next_retry_at,NOW()) END WHERE ticket_id=$1 AND state<>'settled'", ticket, outcome, certainty, usagePending, string(raw))
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return ErrAttemptIdentity
	}
	return nil
}
