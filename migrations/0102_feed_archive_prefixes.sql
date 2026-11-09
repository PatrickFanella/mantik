-- Preserve nanosecond source cuts independently of PostgreSQL timestamp precision.
CREATE TABLE IF NOT EXISTS mantik_feed_archive_prefixes (
    kind text NOT NULL CHECK (kind IN ('publication','admission')),
    registration_id text NOT NULL CHECK (length(registration_id) BETWEEN 1 AND 128),
    source_id text NOT NULL CHECK (length(source_id) BETWEEN 1 AND 256),
    at_seconds bigint NOT NULL,
    at_nanos integer NOT NULL CHECK (at_nanos BETWEEN 0 AND 999999999),
    row_count bigint NOT NULL CHECK (row_count > 0),
    previous_sha256 text NOT NULL CHECK (previous_sha256='' OR previous_sha256 ~ '^[a-f0-9]{64}$'),
    digest text NOT NULL CHECK (digest ~ '^[a-f0-9]{64}$'),
    record_sha256 text NOT NULL CHECK (record_sha256 ~ '^[a-f0-9]{64}$'),
    archive_sequence bigint NOT NULL REFERENCES mantik_feed_archives(sequence),
    PRIMARY KEY(kind,source_id),
    UNIQUE(kind,registration_id,row_count)
);
CREATE INDEX IF NOT EXISTS mantik_feed_archive_prefix_cut ON mantik_feed_archive_prefixes(kind,registration_id,at_seconds DESC,at_nanos DESC,source_id DESC);
CREATE TABLE IF NOT EXISTS mantik_feed_archive_prefix_batches (
    archive_sequence bigint PRIMARY KEY REFERENCES mantik_feed_archives(sequence),
    archive_sha256 text NOT NULL CHECK (archive_sha256 ~ '^[a-f0-9]{64}$'),
    at_seconds bigint NOT NULL,
    at_nanos integer NOT NULL CHECK (at_nanos BETWEEN 0 AND 999999999),
    prefix_count integer NOT NULL CHECK (prefix_count >= 0),
    prefix_sha256 text NOT NULL CHECK (prefix_sha256 ~ '^[a-f0-9]{64}$')
);
CREATE INDEX IF NOT EXISTS mantik_feed_archive_prefix_batch_cut ON mantik_feed_archive_prefix_batches(at_seconds,at_nanos,archive_sequence);
CREATE OR REPLACE FUNCTION mantik_require_feed_archive_prefix_batch() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF NOT EXISTS (SELECT 1 FROM mantik_feed_archive_prefix_batches WHERE archive_sequence=NEW.sequence AND archive_sha256=NEW.sha256) THEN
        RAISE EXCEPTION 'feed archive prefix batch missing';
    END IF;
    RETURN NEW;
END;
$$;
DROP TRIGGER IF EXISTS mantik_feed_archive_prefix_batch_required ON mantik_feed_archives;
CREATE CONSTRAINT TRIGGER mantik_feed_archive_prefix_batch_required
AFTER INSERT ON mantik_feed_archives DEFERRABLE INITIALLY DEFERRED
FOR EACH ROW EXECUTE FUNCTION mantik_require_feed_archive_prefix_batch();
