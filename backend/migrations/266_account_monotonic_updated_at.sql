-- Account snapshots use updated_at to reject delayed cache writers. Application
-- clocks and NOW() (transaction start) cannot order conflicting row updates.
-- This row trigger runs under PostgreSQL's update lock and changes only the
-- timestamp: all business columns, soft deletion and scheduling mode stay intact.
-- No existing rows are rewritten and no historical migration is modified.
CREATE OR REPLACE FUNCTION account_monotonic_updated_at()
RETURNS TRIGGER
LANGUAGE plpgsql
AS $$
BEGIN
    NEW.updated_at := GREATEST(
        OLD.updated_at + INTERVAL '1 microsecond',
        clock_timestamp()
    );
    RETURN NEW;
END;
$$;

-- Run after the existing account BEFORE triggers, which normalize other fields.
-- Including every UPDATE also covers SQL writers that omit updated_at entirely.
DROP TRIGGER IF EXISTS zz_accounts_monotonic_updated_at ON accounts;
CREATE TRIGGER zz_accounts_monotonic_updated_at
BEFORE UPDATE ON accounts
FOR EACH ROW
EXECUTE FUNCTION account_monotonic_updated_at();
