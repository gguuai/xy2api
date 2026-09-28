-- Keep failure feedback in the existing attempt outbox. NULL retry time means
-- no pending work; completed history never participates in the queue scan.
ALTER TABLE scheduling_attempts ADD COLUMN IF NOT EXISTS failure_feedback_done BOOLEAN NOT NULL DEFAULT FALSE;
ALTER TABLE scheduling_attempts ADD COLUMN IF NOT EXISTS failure_feedback_next_retry_at TIMESTAMPTZ;
ALTER TABLE scheduling_attempts ADD COLUMN IF NOT EXISTS failure_feedback_retry_count INTEGER NOT NULL DEFAULT 0 CHECK (failure_feedback_retry_count BETWEEN 0 AND 6);

-- Existing audits are the durable once-per-ticket authority, including feedback
-- committed before its intent was written. Only unaudited intents need replay.
UPDATE scheduling_attempts a
SET failure_feedback_done=TRUE,failure_feedback_next_retry_at=NULL,failure_feedback_retry_count=0
WHERE NOT failure_feedback_done AND EXISTS (
 SELECT 1 FROM scheduling_failure_audit e
 WHERE e.ticket_id=a.ticket_id AND e.action='failure_feedback'
);
UPDATE scheduling_attempts
SET failure_feedback_next_retry_at=NOW()
WHERE NOT failure_feedback_done AND metrics ? 'failure_intent'
 AND failure_feedback_next_retry_at IS NULL;

CREATE INDEX IF NOT EXISTS scheduling_attempts_failure_feedback_pending
 ON scheduling_attempts(failure_feedback_next_retry_at,ticket_id)
 WHERE NOT failure_feedback_done AND failure_feedback_next_retry_at IS NOT NULL;
