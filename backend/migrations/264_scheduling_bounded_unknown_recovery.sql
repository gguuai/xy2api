-- Remote outcome and local admission capacity are different facts. Keep unknown
-- executions and their accounting records, but bound their capacity/probe hold.
ALTER TABLE scheduling_attempts
 ADD COLUMN IF NOT EXISTS local_finished_at TIMESTAMPTZ,
 ADD COLUMN IF NOT EXISTS unknown_hold_until TIMESTAMPTZ;

UPDATE scheduling_attempts
 SET unknown_hold_until=LEAST(lease_until,NOW()+INTERVAL '30 seconds')
 WHERE state='unknown' AND unknown_hold_until IS NULL;

CREATE INDEX IF NOT EXISTS scheduling_attempts_unknown_hold
 ON scheduling_attempts(account_id,unknown_hold_until)
 WHERE state='unknown';
