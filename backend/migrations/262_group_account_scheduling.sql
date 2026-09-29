-- One account priority/weight policy per group; model-specific policies remain
-- untouched as audit data. Group 0 means ungrouped accounts in standard mode,
-- and all accounts in simple mode. The application validates this membership.
CREATE TABLE IF NOT EXISTS scheduling_group_policies (
 group_id BIGINT PRIMARY KEY CHECK (group_id >= 0),
 version BIGINT NOT NULL CHECK (version > 0),
 policy JSONB NOT NULL CHECK (jsonb_typeof(policy) = 'object'),
 updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
