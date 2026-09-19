BEGIN;
CREATE TABLE inbox_messages (
 consumer_name TEXT NOT NULL CHECK(btrim(consumer_name)<>''),
 message_id TEXT NOT NULL CHECK(btrim(message_id)<>''),
 payload_hash BYTEA NOT NULL CHECK(octet_length(payload_hash)=32),
 received_at TIMESTAMPTZ NOT NULL CHECK(isfinite(received_at)),
 completed_at TIMESTAMPTZ CHECK(isfinite(completed_at)),
 transaction_id TEXT REFERENCES wager_transactions(id) ON DELETE RESTRICT,
 PRIMARY KEY(consumer_name,message_id),
 CONSTRAINT inbox_completion_pair CHECK((completed_at IS NULL)=(transaction_id IS NULL))
);
CREATE FUNCTION protect_inbox() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF ROW(NEW.consumer_name,NEW.message_id,NEW.payload_hash,NEW.received_at)
 IS DISTINCT FROM ROW(OLD.consumer_name,OLD.message_id,OLD.payload_hash,OLD.received_at)
 OR (OLD.completed_at IS NOT NULL AND ROW(NEW.completed_at,NEW.transaction_id) IS DISTINCT FROM ROW(OLD.completed_at,OLD.transaction_id)) THEN
 RAISE EXCEPTION 'inbox identity and completed outcome are immutable' USING ERRCODE='23514';
 END IF;
 RETURN NEW;
END;
$$;
CREATE TRIGGER inbox_immutable BEFORE UPDATE ON inbox_messages FOR EACH ROW EXECUTE FUNCTION protect_inbox();
CREATE TRIGGER inbox_no_delete BEFORE DELETE OR TRUNCATE ON inbox_messages FOR EACH STATEMENT EXECUTE FUNCTION reject_financial_record_mutation();
COMMIT;
