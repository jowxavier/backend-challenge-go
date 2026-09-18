BEGIN;
DO $$ BEGIN
    IF EXISTS (SELECT 1 FROM wager_transactions) THEN
        RAISE EXCEPTION 'financial core migration requires an empty wager_transactions dataset; historical replay results cannot be fabricated';
    END IF;
END $$;

ALTER TABLE wager_transactions
    ADD COLUMN payload_hash BYTEA NOT NULL CHECK (octet_length(payload_hash) = 32),
    ADD COLUMN result_balance_minor BIGINT,
    ADD COLUMN result_currency TEXT,
    ADD CONSTRAINT wager_id_wallet_unique UNIQUE (id, wallet_id),
    ADD CONSTRAINT wager_provider_id_unique UNIQUE (provider_id, id),
    ADD CONSTRAINT wager_result_valid CHECK (
        (result_balance_minor IS NULL AND result_currency IS NULL) OR
        (result_balance_minor IS NOT NULL AND result_currency IS NOT NULL AND
         result_balance_minor >= 0 AND result_currency COLLATE "C" ~ '^[A-Z]{3}$')
    ),
    ADD CONSTRAINT wager_result_status CHECK (
        (status IN ('PENDING','PENDING_REFERENCE') AND result_balance_minor IS NULL) OR
        (status IN ('PROCESSED','REJECTED') AND result_balance_minor IS NOT NULL) OR
        status = 'FAILED'
    );
ALTER TABLE wager_transactions DROP CONSTRAINT wager_failure_consistency;
ALTER TABLE wager_transactions ADD CONSTRAINT wager_failure_consistency CHECK (
    (status IN ('PENDING','PENDING_REFERENCE','PROCESSED') AND failure_code IS NULL) OR
    (status = 'REJECTED' AND failure_code IS NOT NULL AND failure_code IN
        ('BET_INSUFFICIENT_FUNDS','REVERSAL_INSUFFICIENT_FUNDS','REFERENCE_NOT_FOUND','CURRENCY_MISMATCH','MONETARY_OVERFLOW')) OR
    (status = 'FAILED' AND failure_code IS NOT NULL AND failure_code = 'PERMANENT_INFRASTRUCTURE_FAILURE')
);

CREATE TABLE financial_idempotency_keys (
    provider_id TEXT NOT NULL CHECK (btrim(provider_id) <> ''),
    idempotency_key TEXT NOT NULL CHECK (btrim(idempotency_key) <> ''),
    transaction_id TEXT NOT NULL,
    PRIMARY KEY (provider_id, idempotency_key),
    FOREIGN KEY (provider_id, transaction_id) REFERENCES wager_transactions(provider_id, id) ON DELETE RESTRICT
);
CREATE INDEX financial_idempotency_transaction_idx ON financial_idempotency_keys(transaction_id);

ALTER TABLE wallets ADD CONSTRAINT wallets_id_currency_unique UNIQUE (id, currency);
CREATE TABLE wallet_ledger_entries (
    id TEXT PRIMARY KEY CHECK (btrim(id) <> ''),
    wallet_id TEXT NOT NULL,
    transaction_id TEXT NOT NULL,
    direction TEXT NOT NULL CHECK (direction IN ('DEBIT','CREDIT')),
    amount_minor BIGINT NOT NULL CHECK (amount_minor > 0),
    currency TEXT NOT NULL CHECK (currency COLLATE "C" ~ '^[A-Z]{3}$'),
    balance_before_minor BIGINT NOT NULL CHECK (balance_before_minor >= 0),
    balance_after_minor BIGINT NOT NULL CHECK (balance_after_minor >= 0),
    created_at TIMESTAMPTZ NOT NULL,
    CONSTRAINT ledger_wallet_transaction_unique UNIQUE (wallet_id, transaction_id),
    FOREIGN KEY (transaction_id, wallet_id) REFERENCES wager_transactions(id, wallet_id) ON DELETE RESTRICT,
    FOREIGN KEY (wallet_id, currency) REFERENCES wallets(id, currency) ON DELETE RESTRICT,
    CONSTRAINT ledger_arithmetic_valid CHECK (
        (direction = 'CREDIT' AND balance_after_minor >= balance_before_minor AND
         balance_after_minor - balance_before_minor = amount_minor) OR
        (direction = 'DEBIT' AND balance_before_minor >= balance_after_minor AND
         balance_before_minor - balance_after_minor = amount_minor)
    )
);

CREATE FUNCTION reject_financial_record_mutation() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    RAISE EXCEPTION '% is append-only', TG_TABLE_NAME USING ERRCODE = '23514';
END;
$$;
CREATE TRIGGER ledger_append_only BEFORE UPDATE OR DELETE OR TRUNCATE ON wallet_ledger_entries
    FOR EACH STATEMENT EXECUTE FUNCTION reject_financial_record_mutation();
CREATE TRIGGER idempotency_keys_append_only BEFORE UPDATE OR DELETE OR TRUNCATE ON financial_idempotency_keys
    FOR EACH STATEMENT EXECUTE FUNCTION reject_financial_record_mutation();
COMMIT;
