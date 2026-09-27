-- Opt-in explicit scheduling; legacy priorities and load_factor retain their meaning.
CREATE TABLE IF NOT EXISTS scheduling_policies (
 group_id BIGINT NOT NULL DEFAULT 0 CHECK (group_id >= 0), model TEXT NOT NULL,
 version BIGINT NOT NULL CHECK (version > 0), policy JSONB NOT NULL,
 updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(), PRIMARY KEY (group_id, model)
);
CREATE TABLE IF NOT EXISTS scheduling_controls (
 scope TEXT NOT NULL CHECK (scope IN ('logical_account','credential_family')),
 subject_id BIGINT NOT NULL REFERENCES accounts(id),
 state TEXT NOT NULL DEFAULT 'RUNNING' CHECK (state IN ('RUNNING','DRAINING','PAUSED','DRAIN_UNCERTAIN')),
 epoch BIGINT NOT NULL DEFAULT 0 CHECK (epoch >= 0), mode TEXT NOT NULL DEFAULT 'request_drain',
 session_deadline TIMESTAMPTZ, session_turn_limit INTEGER NOT NULL DEFAULT 0,
 updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(), PRIMARY KEY (scope, subject_id)
);
CREATE TABLE IF NOT EXISTS scheduling_attempts (
 ticket_id TEXT PRIMARY KEY, request_id TEXT NOT NULL,
 account_id BIGINT NOT NULL REFERENCES accounts(id), family_id BIGINT NOT NULL REFERENCES accounts(id),
 session_id TEXT NOT NULL DEFAULT '', node_id TEXT NOT NULL,
 account_epoch BIGINT NOT NULL, family_epoch BIGINT NOT NULL,
 state TEXT NOT NULL CHECK (state IN ('dispatched','unknown','settled')),
 outcome TEXT NOT NULL DEFAULT '', usage_pending BOOLEAN NOT NULL DEFAULT FALSE,
 usage_acknowledged BOOLEAN NOT NULL DEFAULT FALSE,
 cancel_requested BOOLEAN NOT NULL DEFAULT FALSE, metrics JSONB NOT NULL DEFAULT '{}'::jsonb,
 dispatched_at TIMESTAMPTZ NOT NULL DEFAULT NOW(), lease_until TIMESTAMPTZ NOT NULL, settled_at TIMESTAMPTZ
);
CREATE INDEX IF NOT EXISTS scheduling_attempts_active_account ON scheduling_attempts(account_id, state) WHERE state <> 'settled';
CREATE INDEX IF NOT EXISTS scheduling_attempts_active_family ON scheduling_attempts(family_id, state) WHERE state <> 'settled';
CREATE INDEX IF NOT EXISTS scheduling_attempts_metrics_scope ON scheduling_attempts((metrics->>'group_id'),(metrics->>'model')) WHERE metrics ? 'sent_at';
CREATE INDEX IF NOT EXISTS scheduling_attempts_request ON scheduling_attempts(request_id, dispatched_at, ticket_id);
CREATE INDEX IF NOT EXISTS scheduling_attempts_lease ON scheduling_attempts(lease_until) WHERE state = 'dispatched';
CREATE TABLE IF NOT EXISTS scheduling_owner_sessions (
 account_id BIGINT NOT NULL REFERENCES accounts(id), family_id BIGINT NOT NULL REFERENCES accounts(id),
 session_id TEXT NOT NULL, last_seen_at TIMESTAMPTZ NOT NULL DEFAULT NOW(), PRIMARY KEY (account_id, session_id)
);
CREATE TABLE IF NOT EXISTS scheduling_session_grants (
 scope TEXT NOT NULL, subject_id BIGINT NOT NULL, epoch BIGINT NOT NULL, session_id TEXT NOT NULL,
 remaining_turns INTEGER NOT NULL CHECK (remaining_turns >= 0), expires_at TIMESTAMPTZ NOT NULL,
 PRIMARY KEY (scope, subject_id, epoch, session_id),
 FOREIGN KEY (scope, subject_id) REFERENCES scheduling_controls(scope, subject_id)
);
