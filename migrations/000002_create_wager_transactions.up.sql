BEGIN;
CREATE TABLE wager_transactions (
    id TEXT PRIMARY KEY CHECK (btrim(id) <> ''),
    provider_id TEXT NOT NULL CHECK (btrim(provider_id) <> ''),
    external_transaction_id TEXT NOT NULL CHECK (btrim(external_transaction_id) <> ''),
    player_id TEXT NOT NULL CHECK (btrim(player_id) <> ''),
    wallet_id TEXT NOT NULL REFERENCES wallets(id) ON DELETE RESTRICT CHECK (btrim(wallet_id) <> ''),
    round_id TEXT NOT NULL CHECK (btrim(round_id) <> ''),
    game_id TEXT NOT NULL CHECK (btrim(game_id) <> ''),
    kind TEXT NOT NULL CHECK (kind IN ('BET','WIN','LOSS','REFUND','ROLLBACK')),
    amount_minor BIGINT NOT NULL,
    currency TEXT NOT NULL CHECK (currency COLLATE "C" ~ '^[A-Z]{3}$'),
    reference_external_transaction_id TEXT,
    status TEXT NOT NULL CHECK (status IN ('PENDING','PENDING_REFERENCE','PROCESSED','REJECTED','FAILED')),
    failure_code TEXT,
    created_at TIMESTAMPTZ NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL,
    CONSTRAINT wager_financial_identity_unique UNIQUE (provider_id, external_transaction_id),
    CONSTRAINT wager_amount_valid CHECK (
        (kind = 'LOSS' AND amount_minor = 0) OR
        (kind IN ('BET','WIN','REFUND','ROLLBACK') AND amount_minor > 0)
    ),
    CONSTRAINT wager_reference_valid CHECK (
        reference_external_transaction_id IS NULL OR
        (btrim(reference_external_transaction_id) <> '' AND reference_external_transaction_id <> external_transaction_id)
    ),
    CONSTRAINT wager_reference_presence CHECK (
        (kind IN ('BET','LOSS') AND reference_external_transaction_id IS NULL) OR
        kind = 'WIN' OR
        (kind IN ('REFUND','ROLLBACK') AND reference_external_transaction_id IS NOT NULL)
    ),
    CONSTRAINT wager_pending_reference CHECK (
        status <> 'PENDING_REFERENCE' OR reference_external_transaction_id IS NOT NULL
    ),
    CONSTRAINT wager_failure_consistency CHECK (
        (status IN ('PENDING','PENDING_REFERENCE','PROCESSED') AND failure_code IS NULL) OR
        (status = 'REJECTED' AND failure_code IS NOT NULL AND failure_code IN
            ('BET_INSUFFICIENT_FUNDS','REVERSAL_INSUFFICIENT_FUNDS','REFERENCE_NOT_FOUND')) OR
        (status = 'FAILED' AND failure_code IS NOT NULL AND failure_code = 'PERMANENT_INFRASTRUCTURE_FAILURE')
    )
);
CREATE INDEX wager_transactions_wallet_id_idx ON wager_transactions(wallet_id);
COMMIT;
