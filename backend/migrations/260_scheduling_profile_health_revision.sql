-- Independent semantic health revisions: never reuse an old generation after edits or resets.
CREATE SEQUENCE IF NOT EXISTS scheduling_profile_health_revision_seq AS BIGINT START WITH 1;
