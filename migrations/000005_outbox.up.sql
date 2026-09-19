BEGIN;
CREATE TABLE outbox_events (
 id TEXT PRIMARY KEY CHECK (btrim(id) <> ''),
 transaction_id TEXT NOT NULL REFERENCES wager_transactions(id) ON DELETE RESTRICT,
 aggregate_id TEXT NOT NULL CHECK (btrim(aggregate_id) <> ''),
 event_type TEXT NOT NULL CHECK (event_type IN ('WagerTransactionProcessed','WagerTransactionRejected','WalletBalanceChanged','WagerTransactionPendingReference')),
 event_version INTEGER NOT NULL CHECK (event_version >= 1),
 payload JSONB NOT NULL,
 occurred_at TIMESTAMPTZ NOT NULL CHECK (isfinite(occurred_at)),
 published_at TIMESTAMPTZ CHECK (isfinite(published_at)),
 attempt_count BIGINT NOT NULL DEFAULT 0 CHECK (attempt_count >= 0),
 next_attempt_at TIMESTAMPTZ NOT NULL CHECK (isfinite(next_attempt_at)),
 lease_token TEXT CHECK (btrim(lease_token) <> ''),
 lease_until TIMESTAMPTZ CHECK (isfinite(lease_until)),
 last_error TEXT CHECK (octet_length(last_error) <= 1024),
 CONSTRAINT outbox_logical_event_unique UNIQUE(transaction_id,event_type),
 CONSTRAINT outbox_lease_pair CHECK ((lease_token IS NULL) = (lease_until IS NULL)),
 CONSTRAINT outbox_published_unleased CHECK (published_at IS NULL OR lease_token IS NULL),
 CONSTRAINT outbox_envelope CHECK ((
   jsonb_typeof(payload) = 'object' AND
   payload->'eventId' = to_jsonb(id) AND
   payload->'aggregateId' = to_jsonb(aggregate_id) AND
   payload->'eventType' = to_jsonb(event_type) AND
   payload->'version' = to_jsonb(event_version) AND
   payload->'correlationId' = to_jsonb(transaction_id) AND
   payload->'causationId' = 'null'::jsonb AND
   jsonb_typeof(payload->'occurredAt') = 'string' AND
   jsonb_typeof(payload->'data') = 'object'
 ) IS TRUE)
);
CREATE INDEX outbox_pending_delivery_idx ON outbox_events(next_attempt_at,id) WHERE published_at IS NULL;
CREATE FUNCTION protect_outbox_snapshot() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF ROW(NEW.id,NEW.transaction_id,NEW.aggregate_id,NEW.event_type,NEW.event_version,NEW.payload,NEW.occurred_at)
 IS DISTINCT FROM ROW(OLD.id,OLD.transaction_id,OLD.aggregate_id,OLD.event_type,OLD.event_version,OLD.payload,OLD.occurred_at) THEN
  RAISE EXCEPTION 'outbox event snapshot is immutable' USING ERRCODE='23514';
 END IF;
 RETURN NEW;
END;
$$;
CREATE TRIGGER outbox_snapshot_immutable BEFORE UPDATE ON outbox_events FOR EACH ROW EXECUTE FUNCTION protect_outbox_snapshot();
CREATE TRIGGER outbox_no_delete BEFORE DELETE OR TRUNCATE ON outbox_events FOR EACH STATEMENT EXECUTE FUNCTION reject_financial_record_mutation();
COMMIT;
