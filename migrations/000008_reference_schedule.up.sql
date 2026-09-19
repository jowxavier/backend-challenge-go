BEGIN;
ALTER TABLE wager_transactions ADD COLUMN reference_attempts INTEGER NOT NULL DEFAULT 0 CHECK (reference_attempts >= 0), ADD COLUMN reference_next_attempt_at TIMESTAMPTZ;
CREATE INDEX wager_reference_due_idx ON wager_transactions (reference_next_attempt_at, id) WHERE status = 'PENDING_REFERENCE';
COMMIT;
