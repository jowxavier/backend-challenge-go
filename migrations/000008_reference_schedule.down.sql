BEGIN;
DROP INDEX wager_reference_due_idx;
ALTER TABLE wager_transactions DROP COLUMN reference_next_attempt_at, DROP COLUMN reference_attempts;
COMMIT;
