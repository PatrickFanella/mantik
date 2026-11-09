ALTER TABLE mantik_paper_record_epochs ADD COLUMN IF NOT EXISTS retired_epoch bigint
    CHECK(retired_epoch>created_epoch);
CREATE INDEX IF NOT EXISTS mantik_paper_owner_interval ON mantik_paper_record_epochs
    USING gist (int8range(created_epoch,retired_epoch,'[)'));

-- Interval metadata is separate from original, hash-bearing creation pins.
-- No writer can retire records yet: the immutable epoch guard still applies.
CREATE OR REPLACE FUNCTION mantik_require_paper_archive_batch() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE batch mantik_paper_archive_batches; body jsonb; members integer;
BEGIN
    SELECT * INTO batch FROM mantik_paper_archive_batches WHERE archive_sequence=NEW.sequence;
    IF NOT FOUND THEN RAISE EXCEPTION 'paper archive batch missing' USING ERRCODE='23514'; END IF;
    body := convert_from(NEW.header,'UTF8')::jsonb;
    SELECT count(*) INTO members FROM mantik_paper_archive_members WHERE archive_sequence=NEW.sequence;
    IF batch.archive_sha256<>NEW.sha256 OR batch.source_sha256<>body->>'source_sha256' OR
       batch.records_sha256<>NEW.records_sha256 OR batch.record_count<>members OR
       members<>(SELECT count(*) FROM mantik_paper_record_epochs WHERE int8range(created_epoch,retired_epoch,'[)') @> (NEW.sequence-1)) OR
       members<>(body->>'decisions')::integer+(body->>'outcomes')::integer+(body->>'assessments')::integer+(body->>'reviews')::integer OR
       EXISTS(SELECT 1 FROM mantik_paper_archive_members m JOIN mantik_paper_record_epochs e USING(kind,record_id)
              WHERE m.archive_sequence=NEW.sequence AND (m.created_epoch<>e.created_epoch OR m.record_sha256<>e.record_sha256 OR
                    NOT int8range(e.created_epoch,e.retired_epoch,'[)') @> (NEW.sequence-1))) THEN
        RAISE EXCEPTION 'paper archive batch ownership mismatch' USING ERRCODE='23514';
    END IF;
    RETURN NULL;
END;
$$;

COMMENT ON COLUMN mantik_paper_record_epochs.retired_epoch IS
    'Exclusive end of active ownership; immutable until a separate retirement owner is installed; original creation pins remain unchanged';
