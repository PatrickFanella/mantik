-- The capped equity book is independent of the broker's account balances.
CREATE TABLE IF NOT EXISTS mantik_capped_books (
 id text PRIMARY KEY,
 broker_account text NOT NULL UNIQUE,
 config json NOT NULL CHECK (json_typeof(config)='object' AND config->>'ID'=id AND config->>'BrokerAccountID'=broker_account)
);
CREATE TABLE IF NOT EXISTS mantik_capped_book_events (
 book_id text NOT NULL REFERENCES mantik_capped_books(id),
 sequence bigint NOT NULL CHECK(sequence BETWEEN 1 AND 4096),
 event_id text NOT NULL,
 payload json NOT NULL CHECK(json_typeof(payload)='object' AND payload->>'ID'=event_id),
 PRIMARY KEY(book_id,sequence), UNIQUE(book_id,event_id)
);
CREATE OR REPLACE FUNCTION mantik_capped_book_append() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE previous bigint;
BEGIN
 IF TG_OP<>'INSERT' THEN
  RAISE EXCEPTION 'capped book history is append-only' USING ERRCODE='23514';
 END IF;
 IF TG_TABLE_NAME='mantik_capped_book_events' THEN
  PERFORM id FROM mantik_capped_books WHERE id=NEW.book_id FOR UPDATE;
  SELECT coalesce(max(sequence),0) INTO previous FROM mantik_capped_book_events WHERE book_id=NEW.book_id;
  IF NEW.sequence<>previous+1 THEN
   RAISE EXCEPTION 'capped book sequence is not contiguous' USING ERRCODE='23514';
  END IF;
 END IF;
 RETURN NEW;
END;
$$;
DROP TRIGGER IF EXISTS capped_book_immutable ON mantik_capped_books;
CREATE TRIGGER capped_book_immutable BEFORE UPDATE OR DELETE ON mantik_capped_books FOR EACH ROW EXECUTE FUNCTION mantik_capped_book_append();
DROP TRIGGER IF EXISTS capped_event_append ON mantik_capped_book_events;
CREATE TRIGGER capped_event_append BEFORE INSERT OR UPDATE OR DELETE ON mantik_capped_book_events FOR EACH ROW EXECUTE FUNCTION mantik_capped_book_append();
DROP TRIGGER IF EXISTS capped_book_no_truncate ON mantik_capped_books;
CREATE TRIGGER capped_book_no_truncate BEFORE TRUNCATE ON mantik_capped_books FOR EACH STATEMENT EXECUTE FUNCTION mantik_capped_book_append();
DROP TRIGGER IF EXISTS capped_event_no_truncate ON mantik_capped_book_events;
CREATE TRIGGER capped_event_no_truncate BEFORE TRUNCATE ON mantik_capped_book_events FOR EACH STATEMENT EXECUTE FUNCTION mantik_capped_book_append();
