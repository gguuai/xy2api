-- Explicit administrator-declared relationships; never inferred from URLs.
CREATE TABLE IF NOT EXISTS scheduling_account_failure_domains (
 account_id BIGINT PRIMARY KEY REFERENCES accounts(id),version BIGINT NOT NULL CHECK(version>0),
 quota_pool_id TEXT NOT NULL DEFAULT '' CHECK(length(quota_pool_id)<=128),
 availability_pool_id TEXT NOT NULL DEFAULT '' CHECK(length(availability_pool_id)<=128),
 updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
-- Metadata only: no TTFT, scoring, or second recovery state machine.
-- Durable deadlines allow Redis rebuild without silently forgetting gates.
CREATE TABLE IF NOT EXISTS scheduling_failure_gates (
 gate_key TEXT PRIMARY KEY,scope TEXT NOT NULL,reason TEXT NOT NULL,
 version BIGINT NOT NULL DEFAULT 1,blocked BOOLEAN NOT NULL DEFAULT FALSE,
 ready_after TIMESTAMPTZ,updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
ALTER TABLE scheduling_attempts ADD COLUMN IF NOT EXISTS failure_snapshot JSONB NOT NULL DEFAULT '{}'::jsonb;
ALTER TABLE scheduling_attempts ADD COLUMN IF NOT EXISTS failure_probe_keys TEXT[] NOT NULL DEFAULT '{}';
ALTER TABLE scheduling_attempts ADD COLUMN IF NOT EXISTS resolution_version BIGINT NOT NULL DEFAULT 0;
CREATE INDEX IF NOT EXISTS scheduling_attempts_unsettled_failure_probes ON scheduling_attempts USING GIN(failure_probe_keys) WHERE state<>'settled';
CREATE TABLE IF NOT EXISTS scheduling_failure_audit (
 id BIGSERIAL PRIMARY KEY,account_id BIGINT NOT NULL REFERENCES accounts(id),
 ticket_id TEXT NOT NULL DEFAULT '',actor_id BIGINT NOT NULL DEFAULT 0,
 action TEXT NOT NULL,evidence JSONB NOT NULL,created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE UNIQUE INDEX IF NOT EXISTS scheduling_failure_feedback_once ON scheduling_failure_audit(ticket_id,action) WHERE ticket_id<>'' AND action='failure_feedback';
