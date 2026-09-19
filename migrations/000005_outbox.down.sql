BEGIN;
LOCK TABLE outbox_events IN ACCESS EXCLUSIVE MODE;
DO $$ BEGIN
 IF EXISTS (SELECT 1 FROM outbox_events) THEN
  RAISE EXCEPTION 'outbox contains events; refusing destructive downgrade';
 END IF;
END $$;
DROP TABLE outbox_events;
DROP FUNCTION protect_outbox_snapshot();
COMMIT;
