BEGIN;
-- Refuse a downgrade that old code cannot rehydrate; never rewrite audit records.
DO $$ BEGIN
 IF EXISTS(SELECT 1 FROM wager_transactions WHERE reference_transaction_id IS NOT NULL OR reference_deadline_at IS NOT NULL OR failure_code IN
 ('REFERENCE_NOT_ALLOWED','REFERENCE_MISMATCH','REFERENCE_UNSUCCESSFUL','ALREADY_REVERSED')) THEN
 RAISE EXCEPTION 'reference metadata or failure codes prevent a lossless downgrade';
 END IF;
END $$;
DROP INDEX wager_successful_reversal_unique;
ALTER TABLE wager_transactions
 DROP CONSTRAINT wager_resolved_reference_fk,
 DROP CONSTRAINT wager_reference_identity_unique,
 DROP CONSTRAINT wager_internal_reference_valid,
 DROP CONSTRAINT wager_processed_reference,
 DROP CONSTRAINT wager_pending_deadline,
 DROP CONSTRAINT wager_deadline_reference,
 DROP COLUMN reference_transaction_id,
 DROP COLUMN reference_deadline_at,
 DROP CONSTRAINT wager_failure_consistency;
ALTER TABLE wager_transactions ADD CONSTRAINT wager_failure_consistency CHECK (
 (status IN ('PENDING','PENDING_REFERENCE','PROCESSED') AND failure_code IS NULL) OR
 (status='REJECTED' AND failure_code IS NOT NULL AND failure_code IN
 ('BET_INSUFFICIENT_FUNDS','REVERSAL_INSUFFICIENT_FUNDS','REFERENCE_NOT_FOUND','CURRENCY_MISMATCH','MONETARY_OVERFLOW')) OR
 (status='FAILED' AND failure_code IS NOT NULL AND failure_code='PERMANENT_INFRASTRUCTURE_FAILURE')
);
COMMIT;
