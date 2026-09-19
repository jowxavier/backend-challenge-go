BEGIN;
LOCK TABLE inbox_messages IN ACCESS EXCLUSIVE MODE;
DO $$ BEGIN
 IF EXISTS(SELECT 1 FROM inbox_messages) THEN
 RAISE EXCEPTION 'inbox contains records; refusing destructive downgrade';
 END IF;
END $$;
DROP TABLE inbox_messages;
DROP FUNCTION protect_inbox();
COMMIT;
