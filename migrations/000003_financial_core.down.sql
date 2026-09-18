BEGIN;
DROP TABLE wallet_ledger_entries;
DROP TABLE financial_idempotency_keys;
DROP FUNCTION reject_financial_record_mutation();
ALTER TABLE wallets DROP CONSTRAINT wallets_id_currency_unique;
-- Old code cannot rehydrate the new failure codes. Refuse an unsafe downgrade.
DO $$ BEGIN
    IF EXISTS (SELECT 1 FROM wager_transactions WHERE failure_code IN ('CURRENCY_MISMATCH','MONETARY_OVERFLOW')) THEN
        RAISE EXCEPTION 'remove disposable financial-core data before downgrade';
    END IF;
END $$;
ALTER TABLE wager_transactions
    DROP CONSTRAINT wager_failure_consistency,
    DROP CONSTRAINT wager_result_status,
    DROP CONSTRAINT wager_result_valid,
    DROP CONSTRAINT wager_id_wallet_unique,
    DROP CONSTRAINT wager_provider_id_unique,
    DROP COLUMN payload_hash,
    DROP COLUMN result_balance_minor,
    DROP COLUMN result_currency;
ALTER TABLE wager_transactions ADD CONSTRAINT wager_failure_consistency CHECK (
    (status IN ('PENDING','PENDING_REFERENCE','PROCESSED') AND failure_code IS NULL) OR
    (status = 'REJECTED' AND failure_code IS NOT NULL AND failure_code IN
        ('BET_INSUFFICIENT_FUNDS','REVERSAL_INSUFFICIENT_FUNDS','REFERENCE_NOT_FOUND')) OR
    (status = 'FAILED' AND failure_code IS NOT NULL AND failure_code = 'PERMANENT_INFRASTRUCTURE_FAILURE')
);
COMMIT;
