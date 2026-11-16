-- Legacy commands remain byte-for-byte in the immutable, capped v121 table.
-- New commands live in indexed immutable custody; only the bounded replay head
-- is replaceable. Sequence/version counts original commands, not hot slots.
CREATE TABLE IF NOT EXISTS mantik_capped_book_history (
 book_id text NOT NULL REFERENCES mantik_capped_books(id),
 sequence bigint NOT NULL CHECK(sequence>0),
 event_id text NOT NULL,
 payload json NOT NULL CHECK(json_typeof(payload)='object' AND payload->>'ID'=event_id AND octet_length(payload::text)<=4194304),
 previous_sha256 text NOT NULL CHECK(previous_sha256 ~ '^[a-f0-9]{64}$'),
 state_sha256 text NOT NULL CHECK(state_sha256 ~ '^[a-f0-9]{64}$'),
 PRIMARY KEY(book_id,sequence), UNIQUE(book_id,event_id), UNIQUE(book_id,sequence,state_sha256)
);
CREATE TABLE IF NOT EXISTS mantik_capped_book_heads (
 book_id text PRIMARY KEY REFERENCES mantik_capped_books(id),
 version bigint NOT NULL,
 state json NOT NULL CHECK(json_typeof(state)='object' AND state->'Config'->>'ID'=book_id AND (state->>'Version')::bigint=version AND json_array_length(state->'Events')<=4096),
 state_sha256 text NOT NULL CHECK(state_sha256=encode(sha256(convert_to(state::text,'UTF8')),'hex')),
 FOREIGN KEY(book_id,version,state_sha256) REFERENCES mantik_capped_book_history(book_id,sequence,state_sha256)
);
CREATE OR REPLACE FUNCTION mantik_capped_history_guard() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE previous bigint; prior_hash text;
BEGIN
 IF TG_OP='TRUNCATE' OR TG_OP='DELETE' OR (TG_OP='UPDATE' AND TG_TABLE_NAME<>'mantik_capped_book_heads') THEN
  RAISE EXCEPTION 'capped source history is immutable' USING ERRCODE='23514';
 END IF;
 PERFORM id FROM mantik_capped_books WHERE id=NEW.book_id FOR UPDATE;
 IF TG_TABLE_NAME='mantik_capped_book_events' THEN
  IF EXISTS(SELECT 1 FROM mantik_capped_book_history WHERE book_id=NEW.book_id) THEN
   RAISE EXCEPTION 'legacy capped history is sealed' USING ERRCODE='23514';
  END IF;
 ELSIF TG_TABLE_NAME='mantik_capped_book_history' THEN
  SELECT version,state_sha256 INTO previous,prior_hash FROM mantik_capped_book_heads WHERE book_id=NEW.book_id;
  IF previous IS NULL THEN
   SELECT coalesce(max(sequence),0) INTO previous FROM mantik_capped_book_events WHERE book_id=NEW.book_id;
  END IF;
  IF NEW.sequence<>previous+1 OR (prior_hash IS NOT NULL AND NEW.previous_sha256<>prior_hash) OR
     EXISTS(SELECT 1 FROM mantik_capped_book_events WHERE book_id=NEW.book_id AND event_id=NEW.event_id) THEN
   RAISE EXCEPTION 'capped history sequence or source conflicts' USING ERRCODE='23514';
  END IF;
 ELSE
  IF TG_OP='UPDATE' AND (NEW.book_id<>OLD.book_id OR NEW.version<>OLD.version+1) THEN
   RAISE EXCEPTION 'capped head version conflicts' USING ERRCODE='23514';
  END IF;
 END IF;
 RETURN NEW;
END;
$$;
DROP TRIGGER IF EXISTS capped_legacy_sealed ON mantik_capped_book_events;
CREATE TRIGGER capped_legacy_sealed BEFORE INSERT ON mantik_capped_book_events FOR EACH ROW EXECUTE FUNCTION mantik_capped_history_guard();
DROP TRIGGER IF EXISTS capped_history_immutable ON mantik_capped_book_history;
CREATE TRIGGER capped_history_immutable BEFORE INSERT OR UPDATE OR DELETE ON mantik_capped_book_history FOR EACH ROW EXECUTE FUNCTION mantik_capped_history_guard();
DROP TRIGGER IF EXISTS capped_history_no_truncate ON mantik_capped_book_history;
CREATE TRIGGER capped_history_no_truncate BEFORE TRUNCATE ON mantik_capped_book_history FOR EACH STATEMENT EXECUTE FUNCTION mantik_capped_history_guard();
DROP TRIGGER IF EXISTS capped_head_guard ON mantik_capped_book_heads;
CREATE TRIGGER capped_head_guard BEFORE INSERT OR UPDATE OR DELETE ON mantik_capped_book_heads FOR EACH ROW EXECUTE FUNCTION mantik_capped_history_guard();
DROP TRIGGER IF EXISTS capped_head_no_truncate ON mantik_capped_book_heads;
CREATE TRIGGER capped_head_no_truncate BEFORE TRUNCATE ON mantik_capped_book_heads FOR EACH STATEMENT EXECUTE FUNCTION mantik_capped_history_guard();
-- Reject mutation statements even when a legacy table has no rows yet.
DROP TRIGGER IF EXISTS capped_legacy_no_mutation ON mantik_capped_book_events;
CREATE TRIGGER capped_legacy_no_mutation BEFORE UPDATE OR DELETE ON mantik_capped_book_events FOR EACH STATEMENT EXECUTE FUNCTION mantik_capped_history_guard();
