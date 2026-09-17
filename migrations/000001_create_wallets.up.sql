BEGIN;
CREATE TABLE wallets (
    id TEXT PRIMARY KEY CHECK (btrim(id) <> ''),
    player_id TEXT NOT NULL CHECK (btrim(player_id) <> ''),
    currency TEXT NOT NULL CHECK (currency COLLATE "C" ~ '^[A-Z]{3}$'),
    balance_minor BIGINT NOT NULL CHECK (balance_minor >= 0),
    version BIGINT NOT NULL CHECK (version >= 1),
    created_at TIMESTAMPTZ NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL,
    CONSTRAINT wallets_player_currency_unique UNIQUE (player_id, currency)
);
COMMIT;
