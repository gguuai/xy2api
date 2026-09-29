-- Collapse the retired account-local pause into the original scheduling switch.
-- Never infer enablement or propagate a credential-family pause to other accounts.
-- Keep controls, grants and shared-domain configuration as historical records;
-- current dispatch reads only accounts.schedulable and account-local health.
UPDATE accounts a
SET schedulable = FALSE, updated_at = NOW()
WHERE a.deleted_at IS NULL AND a.schedulable IS TRUE
  AND EXISTS (
    SELECT 1 FROM scheduling_controls c
    WHERE c.scope = 'logical_account' AND c.subject_id = a.id AND c.state <> 'RUNNING'
  );
