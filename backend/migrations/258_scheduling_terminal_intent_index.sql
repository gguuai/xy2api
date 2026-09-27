-- Bound terminal-intent reconciliation scans to the active, oldest pending rows.
CREATE INDEX IF NOT EXISTS scheduling_attempts_terminal_intent
ON scheduling_attempts(dispatched_at, ticket_id)
WHERE state <> 'settled' AND metrics->>'terminal_intent' = 'true';
