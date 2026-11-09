CREATE TABLE IF NOT EXISTS mantik_feed_retention (
    sequence bigint PRIMARY KEY CHECK(sequence>0),
    checkpoint_sequence bigint NOT NULL UNIQUE REFERENCES mantik_paper_feed_checkpoints(sequence),
    sha256 text NOT NULL UNIQUE CHECK(sha256 ~ '^[a-f0-9]{64}$'),
    payload bytea NOT NULL CHECK(octet_length(payload)<=2097152)
);
CREATE TABLE IF NOT EXISTS mantik_paper_feed_source_seals (
    kind text NOT NULL CHECK(kind IN ('decision','outcome')),
    record_id text NOT NULL,
    retention_sequence bigint NOT NULL REFERENCES mantik_feed_retention(sequence),
    record_sha256 text NOT NULL UNIQUE CHECK(record_sha256 ~ '^[a-f0-9]{64}$'),
    basis_sha256 text NOT NULL CHECK(basis_sha256 ~ '^[a-f0-9]{64}$'),
    payload bytea NOT NULL CHECK(octet_length(payload)<=2048),
    PRIMARY KEY(kind,record_id)
);

-- Collector state carries the owner-approved retirement root. Running older
-- writers discard that new field when decoding, so they must fail atomically.
CREATE OR REPLACE FUNCTION mantik_require_feed_retention_root() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE expected text;
BEGIN
    SELECT sha256 INTO expected FROM mantik_feed_retention ORDER BY sequence DESC LIMIT 1;
    IF coalesce(NEW.payload->>'archive_sha256','')<>coalesce(expected,'') THEN
        RAISE EXCEPTION 'feed retention owner root missing or changed' USING ERRCODE='23514';
    END IF;
    RETURN NEW;
END;
$$;
DROP TRIGGER IF EXISTS mantik_collector_retention_root ON mantik_feed_collectors;
CREATE TRIGGER mantik_collector_retention_root BEFORE UPDATE ON mantik_feed_collectors FOR EACH ROW EXECUTE FUNCTION mantik_require_feed_retention_root();

CREATE OR REPLACE FUNCTION mantik_require_archive_retention_root() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE expected text;
BEGIN
    SELECT sha256 INTO expected FROM mantik_feed_retention ORDER BY sequence DESC LIMIT 1;
    IF coalesce(convert_from(NEW.payload,'UTF8')::jsonb#>>'{source,retention,sha256}','')<>coalesce(expected,'') THEN
        RAISE EXCEPTION 'archive retention owner root missing or changed' USING ERRCODE='23514';
    END IF;
    RETURN NEW;
END;
$$;
DROP TRIGGER IF EXISTS mantik_archive_retention_root ON mantik_feed_archives;
CREATE TRIGGER mantik_archive_retention_root BEFORE INSERT ON mantik_feed_archives FOR EACH ROW EXECUTE FUNCTION mantik_require_archive_retention_root();

CREATE OR REPLACE FUNCTION mantik_require_paper_feed_source_seal() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE body jsonb;
BEGIN
    IF NOT EXISTS(SELECT 1 FROM mantik_feed_retention) THEN RETURN NULL; END IF;
    body := convert_from(NEW.payload,'UTF8')::jsonb->'record';
    IF NOT EXISTS(SELECT 1 FROM mantik_paper_feed_source_seals WHERE kind=TG_ARGV[0] AND record_id=NEW.id AND record_sha256=body->>'sha256' AND basis_sha256=body->>'basis_sha256') THEN
        RAISE EXCEPTION 'retired feed paper source seal missing' USING ERRCODE='23514';
    END IF;
    RETURN NULL;
END;
$$;
DROP TRIGGER IF EXISTS mantik_paper_decision_source_seal ON mantik_paper_decisions;
CREATE CONSTRAINT TRIGGER mantik_paper_decision_source_seal AFTER INSERT ON mantik_paper_decisions DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION mantik_require_paper_feed_source_seal('decision');
DROP TRIGGER IF EXISTS mantik_paper_outcome_source_seal ON mantik_paper_outcomes;
CREATE CONSTRAINT TRIGGER mantik_paper_outcome_source_seal AFTER INSERT ON mantik_paper_outcomes DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION mantik_require_paper_feed_source_seal('outcome');

-- These indexes are custody of original identities, including negative lookup
-- for a new append. Existing rows cannot be rekeyed, replaced or removed.
CREATE OR REPLACE FUNCTION mantik_immutable_feed_archive_index() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    RAISE EXCEPTION 'feed archive identity index is immutable' USING ERRCODE='23514';
END;
$$;
DROP TRIGGER IF EXISTS mantik_immutable_feed_sources ON mantik_feed_archive_sources;
CREATE TRIGGER mantik_immutable_feed_sources BEFORE UPDATE OR DELETE ON mantik_feed_archive_sources FOR EACH ROW EXECUTE FUNCTION mantik_immutable_feed_archive_index();
DROP TRIGGER IF EXISTS mantik_immutable_feed_prefixes ON mantik_feed_archive_prefixes;
CREATE TRIGGER mantik_immutable_feed_prefixes BEFORE UPDATE OR DELETE ON mantik_feed_archive_prefixes FOR EACH ROW EXECUTE FUNCTION mantik_immutable_feed_archive_index();

COMMENT ON TABLE mantik_paper_decisions IS 'v4 archive-aware windows use v3 selection and digest rules with canonical recording metadata; all earlier payloads and hashes remain unchanged';
COMMENT ON TABLE mantik_paper_outcomes IS 'v4 archive-aware windows retain complete selected original evidence with canonical recording metadata; older schemas preserve their original hashes';
