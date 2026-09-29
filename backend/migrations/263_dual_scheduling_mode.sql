-- Keep the deployed controlled scheduler until the administrator explicitly
-- chooses Sub2API. This table is deliberately outside generic settings PUT.
CREATE TABLE IF NOT EXISTS scheduling_system_mode (
 singleton BOOLEAN PRIMARY KEY DEFAULT TRUE CHECK (singleton),
 mode TEXT NOT NULL CHECK (mode IN ('sub2api','controlled')),
 version BIGINT NOT NULL CHECK (version > 0),
 updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- Historical controlled pause also wrote accounts.schedulable. Its previous
-- generic value was not recorded: even equal update timestamps cannot prove an
-- account was enabled before pausing. Preserve every existing disable rather
-- than risk re-enabling an administrator-disabled credential. New controls are
-- fully independent; legacy disabled accounts require administrator review.
