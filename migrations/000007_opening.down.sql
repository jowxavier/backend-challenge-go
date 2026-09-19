BEGIN;
LOCK TABLE wager_transactions IN ACCESS EXCLUSIVE MODE;
DO $$ BEGIN
 IF EXISTS(SELECT 1 FROM wager_transactions WHERE kind='OPENING') THEN RAISE EXCEPTION 'opening history prevents downgrade'; END IF;
END $$;
DROP INDEX wager_opening_wallet_unique;
ALTER TABLE wager_transactions
 DROP CONSTRAINT wager_origin_valid,
 DROP CONSTRAINT wager_kind_valid,
 DROP CONSTRAINT wager_amount_valid,
 DROP CONSTRAINT wager_reference_presence,
 ALTER COLUMN provider_id SET NOT NULL,
 ALTER COLUMN external_transaction_id SET NOT NULL,
 ALTER COLUMN round_id SET NOT NULL,
 ALTER COLUMN game_id SET NOT NULL,
 ALTER COLUMN payload_hash SET NOT NULL,
 ADD CONSTRAINT wager_transactions_kind_check CHECK(kind IN ('BET','WIN','LOSS','REFUND','ROLLBACK')),
 ADD CONSTRAINT wager_amount_valid CHECK((kind='LOSS' AND amount_minor=0) OR (kind IN ('BET','WIN','REFUND','ROLLBACK') AND amount_minor>0)),
 ADD CONSTRAINT wager_reference_presence CHECK((kind IN ('BET','LOSS') AND reference_external_transaction_id IS NULL) OR kind='WIN' OR (kind IN ('REFUND','ROLLBACK') AND reference_external_transaction_id IS NOT NULL));
COMMIT;
