CREATE TABLE IF NOT EXISTS mantik_feed_archive_sources (
    kind text NOT NULL CHECK (kind IN ('publication','admission','input','command')),
    source_id text NOT NULL CHECK (length(source_id) BETWEEN 1 AND 256),
    archive_sequence bigint NOT NULL REFERENCES mantik_feed_archives(sequence),
    record_sha256 text NOT NULL CHECK (record_sha256 ~ '^[a-f0-9]{64}$'),
    PRIMARY KEY(kind,source_id)
);
CREATE TABLE IF NOT EXISTS mantik_feed_archive_source_batches (
    archive_sequence bigint PRIMARY KEY REFERENCES mantik_feed_archives(sequence),
    archive_sha256 text NOT NULL CHECK (archive_sha256 ~ '^[a-f0-9]{64}$'),
    source_count integer NOT NULL CHECK (source_count >= 0),
    source_sha256 text NOT NULL CHECK (source_sha256 ~ '^[a-f0-9]{64}$')
);
-- Older running archive writers cannot commit a snapshot without indexing it.
CREATE OR REPLACE FUNCTION mantik_require_feed_archive_source_batch() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF NOT EXISTS (SELECT 1 FROM mantik_feed_archive_source_batches WHERE archive_sequence=NEW.sequence AND archive_sha256=NEW.sha256) THEN
        RAISE EXCEPTION 'feed archive source batch missing';
    END IF;
    RETURN NEW;
END;
$$;
DROP TRIGGER IF EXISTS mantik_feed_archive_source_batch_required ON mantik_feed_archives;
CREATE CONSTRAINT TRIGGER mantik_feed_archive_source_batch_required
AFTER INSERT ON mantik_feed_archives DEFERRABLE INITIALLY DEFERRED
FOR EACH ROW EXECUTE FUNCTION mantik_require_feed_archive_source_batch();
