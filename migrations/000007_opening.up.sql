BEGIN;
ALTER TABLE wager_transactions
 ALTER COLUMN provider_id DROP NOT NULL,
 ALTER COLUMN external_transaction_id DROP NOT NULL,
 ALTER COLUMN round_id DROP NOT NULL,
 ALTER COLUMN game_id DROP NOT NULL,
 ALTER COLUMN payload_hash DROP NOT NULL,
 DROP CONSTRAINT wager_transactions_kind_check,
 DROP CONSTRAINT wager_amount_valid,
 DROP CONSTRAINT wager_reference_presence,
 ADD CONSTRAINT wager_kind_valid CHECK(kind IN ('OPENING','BET','WIN','LOSS','REFUND','ROLLBACK')),
 ADD CONSTRAINT wager_amount_valid CHECK((kind='LOSS' AND amount_minor=0) OR (kind IN ('OPENING','BET','WIN','REFUND','ROLLBACK') AND amount_minor>0)),
 ADD CONSTRAINT wager_reference_presence CHECK((kind IN ('OPENING','BET','LOSS') AND reference_external_transaction_id IS NULL) OR kind='WIN' OR (kind IN ('REFUND','ROLLBACK') AND reference_external_transaction_id IS NOT NULL)),
 ADD CONSTRAINT wager_origin_valid CHECK(
 (kind='OPENING' AND provider_id IS NULL AND external_transaction_id IS NULL AND round_id IS NULL AND game_id IS NULL AND payload_hash IS NULL AND reference_external_transaction_id IS NULL AND reference_transaction_id IS NULL AND reference_deadline_at IS NULL AND status='PROCESSED' AND failure_code IS NULL AND result_balance_minor=amount_minor AND result_currency=currency)
 OR
 (kind<>'OPENING' AND provider_id IS NOT NULL AND external_transaction_id IS NOT NULL AND round_id IS NOT NULL AND game_id IS NOT NULL AND payload_hash IS NOT NULL)
 );
CREATE UNIQUE INDEX wager_opening_wallet_unique ON wager_transactions(wallet_id) WHERE kind='OPENING';
COMMIT;
