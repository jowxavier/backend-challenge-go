BEGIN;
ALTER TABLE wager_transactions
 ADD COLUMN reference_transaction_id TEXT,
 ADD COLUMN reference_deadline_at TIMESTAMPTZ,
 ADD CONSTRAINT wager_reference_identity_unique UNIQUE(provider_id,external_transaction_id,id),
 ADD CONSTRAINT wager_resolved_reference_fk FOREIGN KEY(provider_id,reference_external_transaction_id,reference_transaction_id)
   REFERENCES wager_transactions(provider_id,external_transaction_id,id) ON DELETE RESTRICT,
 ADD CONSTRAINT wager_internal_reference_valid CHECK(reference_transaction_id IS NULL OR
   (reference_external_transaction_id IS NOT NULL AND btrim(reference_transaction_id)<>'' AND reference_transaction_id<>id)),
 ADD CONSTRAINT wager_processed_reference CHECK(status<>'PROCESSED' OR reference_external_transaction_id IS NULL OR reference_transaction_id IS NOT NULL),
 ADD CONSTRAINT wager_pending_deadline CHECK(status<>'PENDING_REFERENCE' OR reference_deadline_at IS NOT NULL),
 ADD CONSTRAINT wager_deadline_reference CHECK(reference_deadline_at IS NULL OR (reference_external_transaction_id IS NOT NULL AND isfinite(reference_deadline_at)));
CREATE UNIQUE INDEX wager_successful_reversal_unique ON wager_transactions(provider_id,reference_external_transaction_id)
 WHERE status='PROCESSED' AND kind IN ('REFUND','ROLLBACK');
ALTER TABLE wager_transactions DROP CONSTRAINT wager_failure_consistency;
ALTER TABLE wager_transactions ADD CONSTRAINT wager_failure_consistency CHECK (
 (status IN ('PENDING','PENDING_REFERENCE','PROCESSED') AND failure_code IS NULL) OR
 (status='REJECTED' AND failure_code IS NOT NULL AND failure_code IN
 ('BET_INSUFFICIENT_FUNDS','REVERSAL_INSUFFICIENT_FUNDS','REFERENCE_NOT_FOUND','CURRENCY_MISMATCH','MONETARY_OVERFLOW',
 'REFERENCE_NOT_ALLOWED','REFERENCE_MISMATCH','REFERENCE_UNSUCCESSFUL','ALREADY_REVERSED')) OR
 (status='FAILED' AND failure_code IS NOT NULL AND failure_code='PERMANENT_INFRASTRUCTURE_FAILURE')
);
COMMIT;
