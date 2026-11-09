CREATE TABLE IF NOT EXISTS mantik_paper_record_archives (
    sequence bigint PRIMARY KEY CHECK(sequence>0),
    sha256 text NOT NULL UNIQUE CHECK(sha256 ~ '^[a-f0-9]{64}$'),
    records_sha256 text NOT NULL CHECK(records_sha256 ~ '^[a-f0-9]{64}$'),
    header bytea NOT NULL CHECK(octet_length(header)<=4096),
    payload bytea NOT NULL CHECK(octet_length(payload)<=67108864)
);
CREATE TABLE IF NOT EXISTS mantik_paper_record_epochs (
    kind text NOT NULL CHECK(kind IN ('decision','outcome','assessment','review')),
    record_id text NOT NULL,
    created_epoch bigint NOT NULL CHECK(created_epoch>=0),
    record_sha256 text NOT NULL CHECK(record_sha256 ~ '^[a-f0-9]{64}$'),
    at_seconds bigint NOT NULL,
    at_nanos integer NOT NULL CHECK(at_nanos BETWEEN 0 AND 999999999),
    PRIMARY KEY(kind,record_id)
);
CREATE INDEX IF NOT EXISTS mantik_paper_record_epoch_cut ON mantik_paper_record_epochs(created_epoch,kind,at_seconds,at_nanos,record_id);
CREATE TABLE IF NOT EXISTS mantik_paper_archive_members (
    archive_sequence bigint NOT NULL REFERENCES mantik_paper_record_archives(sequence),
    kind text NOT NULL,
    record_id text NOT NULL,
    created_epoch bigint NOT NULL CHECK(created_epoch>=0 AND created_epoch<archive_sequence),
    record_sha256 text NOT NULL,
    payload_sha256 text NOT NULL CHECK(payload_sha256 ~ '^[a-f0-9]{64}$'),
    PRIMARY KEY(archive_sequence,kind,record_id),
    FOREIGN KEY(kind,record_id) REFERENCES mantik_paper_record_epochs(kind,record_id)
);
CREATE INDEX IF NOT EXISTS mantik_paper_archive_original_id ON mantik_paper_archive_members(kind,record_id,archive_sequence);
CREATE TABLE IF NOT EXISTS mantik_paper_archive_batches (
    archive_sequence bigint PRIMARY KEY REFERENCES mantik_paper_record_archives(sequence),
    archive_sha256 text NOT NULL CHECK(archive_sha256 ~ '^[a-f0-9]{64}$'),
    source_sha256 text NOT NULL CHECK(source_sha256 ~ '^[a-f0-9]{64}$'),
    records_sha256 text NOT NULL CHECK(records_sha256 ~ '^[a-f0-9]{64}$'),
    record_count integer NOT NULL CHECK(record_count BETWEEN 0 AND 592)
);

-- Creation order is an owner epoch, independent of event-clock precision.
-- Older running writers also reserve identities in this same transaction.
CREATE OR REPLACE FUNCTION mantik_record_paper_epoch() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE body jsonb; record jsonb; event_time text; epoch bigint;
BEGIN
    body := convert_from(NEW.payload,'UTF8')::jsonb;
    record := CASE WHEN TG_ARGV[2]='record' THEN body->'record' ELSE body END;
    event_time := record->>TG_ARGV[1];
    IF event_time IS NULL OR event_time !~ '^[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}:[0-9]{2}:[0-9]{2}([.][0-9]{1,9})?Z$' THEN
        RAISE EXCEPTION 'invalid paper owner event clock' USING ERRCODE='23514';
    END IF;
    SELECT coalesce(max(sequence),0) INTO epoch FROM mantik_paper_record_archives;
    INSERT INTO mantik_paper_record_epochs(kind,record_id,created_epoch,record_sha256,at_seconds,at_nanos)
    VALUES(TG_ARGV[0],NEW.id,epoch,record->>'sha256',
        extract(epoch FROM regexp_replace(event_time,'[.][0-9]+Z$','Z')::timestamptz)::bigint,
        rpad(coalesce(substring(event_time FROM '[.]([0-9]{1,9})Z$'),'0'),9,'0')::integer);
    RETURN NEW;
END;
$$;
DROP TRIGGER IF EXISTS mantik_decision_creation_epoch ON mantik_paper_decisions;
CREATE TRIGGER mantik_decision_creation_epoch BEFORE INSERT ON mantik_paper_decisions FOR EACH ROW EXECUTE FUNCTION mantik_record_paper_epoch('decision','finalized_at','record');
DROP TRIGGER IF EXISTS mantik_outcome_creation_epoch ON mantik_paper_outcomes;
CREATE TRIGGER mantik_outcome_creation_epoch BEFORE INSERT ON mantik_paper_outcomes FOR EACH ROW EXECUTE FUNCTION mantik_record_paper_epoch('outcome','evaluated_at','record');
DROP TRIGGER IF EXISTS mantik_assessment_creation_epoch ON mantik_paper_evaluation_assessments;
CREATE TRIGGER mantik_assessment_creation_epoch BEFORE INSERT ON mantik_paper_evaluation_assessments FOR EACH ROW EXECUTE FUNCTION mantik_record_paper_epoch('assessment','assessed_at','');
DROP TRIGGER IF EXISTS mantik_review_creation_epoch ON mantik_decision_reviews;
CREATE TRIGGER mantik_review_creation_epoch BEFORE INSERT ON mantik_decision_reviews FOR EACH ROW EXECUTE FUNCTION mantik_record_paper_epoch('review','reviewed_at','');

-- Preserve predecessor envelopes and hashes. Populated replay cannot guess a
-- missing creation epoch or replace an existing identity reservation.
DO $$
BEGIN
    IF EXISTS(SELECT 1 FROM mantik_paper_record_archives) AND EXISTS(
        SELECT 1 FROM (
            SELECT 'decision' AS kind,id FROM mantik_paper_decisions
            UNION ALL SELECT 'outcome',id FROM mantik_paper_outcomes
            UNION ALL SELECT 'assessment',id FROM mantik_paper_evaluation_assessments
            UNION ALL SELECT 'review',id FROM mantik_decision_reviews
        ) a LEFT JOIN mantik_paper_record_epochs e ON e.kind=a.kind AND e.record_id=a.id
        WHERE e.kind IS NULL
    ) THEN
        RAISE EXCEPTION 'paper creation epoch missing from populated archive' USING ERRCODE='23514';
    END IF;
END;
$$;
INSERT INTO mantik_paper_record_epochs(kind,record_id,created_epoch,record_sha256,at_seconds,at_nanos)
SELECT kind,id,0,record->>'sha256',
    extract(epoch FROM regexp_replace(record->>clock,'[.][0-9]+Z$','Z')::timestamptz)::bigint,
    rpad(coalesce(substring(record->>clock FROM '[.]([0-9]{1,9})Z$'),'0'),9,'0')::integer
FROM (
    SELECT 'decision' AS kind,id,convert_from(payload,'UTF8')::jsonb->'record' AS record,'finalized_at' AS clock FROM mantik_paper_decisions
    UNION ALL SELECT 'outcome',id,convert_from(payload,'UTF8')::jsonb->'record','evaluated_at' FROM mantik_paper_outcomes
    UNION ALL SELECT 'assessment',id,convert_from(payload,'UTF8')::jsonb,'assessed_at' FROM mantik_paper_evaluation_assessments
    UNION ALL SELECT 'review',id,convert_from(payload,'UTF8')::jsonb,'reviewed_at' FROM mantik_decision_reviews
) AS original ON CONFLICT(kind,record_id) DO NOTHING;

CREATE OR REPLACE FUNCTION mantik_immutable_paper_archive() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    RAISE EXCEPTION 'paper archive owner record is immutable' USING ERRCODE='23514';
END;
$$;
DROP TRIGGER IF EXISTS mantik_immutable_paper_archives ON mantik_paper_record_archives;
CREATE TRIGGER mantik_immutable_paper_archives BEFORE UPDATE OR DELETE ON mantik_paper_record_archives FOR EACH ROW EXECUTE FUNCTION mantik_immutable_paper_archive();
DROP TRIGGER IF EXISTS mantik_immutable_paper_epochs ON mantik_paper_record_epochs;
CREATE TRIGGER mantik_immutable_paper_epochs BEFORE UPDATE OR DELETE ON mantik_paper_record_epochs FOR EACH ROW EXECUTE FUNCTION mantik_immutable_paper_archive();
DROP TRIGGER IF EXISTS mantik_immutable_paper_members ON mantik_paper_archive_members;
CREATE TRIGGER mantik_immutable_paper_members BEFORE UPDATE OR DELETE ON mantik_paper_archive_members FOR EACH ROW EXECUTE FUNCTION mantik_immutable_paper_archive();
DROP TRIGGER IF EXISTS mantik_immutable_paper_batches ON mantik_paper_archive_batches;
CREATE TRIGGER mantik_immutable_paper_batches BEFORE UPDATE OR DELETE ON mantik_paper_archive_batches FOR EACH ROW EXECUTE FUNCTION mantik_immutable_paper_archive();

CREATE OR REPLACE FUNCTION mantik_require_paper_archive_batch() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE batch mantik_paper_archive_batches; body jsonb; members integer;
BEGIN
    SELECT * INTO batch FROM mantik_paper_archive_batches WHERE archive_sequence=NEW.sequence;
    IF NOT FOUND THEN RAISE EXCEPTION 'paper archive batch missing' USING ERRCODE='23514'; END IF;
    body := convert_from(NEW.header,'UTF8')::jsonb;
    SELECT count(*) INTO members FROM mantik_paper_archive_members WHERE archive_sequence=NEW.sequence;
    IF batch.archive_sha256<>NEW.sha256 OR batch.source_sha256<>body->>'source_sha256' OR
       batch.records_sha256<>NEW.records_sha256 OR batch.record_count<>members OR
       members<>(SELECT count(*) FROM mantik_paper_record_epochs WHERE created_epoch<NEW.sequence) OR
       members<>(body->>'decisions')::integer+(body->>'outcomes')::integer+(body->>'assessments')::integer+(body->>'reviews')::integer OR
       EXISTS(SELECT 1 FROM mantik_paper_archive_members m JOIN mantik_paper_record_epochs e USING(kind,record_id)
              WHERE m.archive_sequence=NEW.sequence AND (m.created_epoch<>e.created_epoch OR m.record_sha256<>e.record_sha256)) THEN
        RAISE EXCEPTION 'paper archive batch ownership mismatch' USING ERRCODE='23514';
    END IF;
    RETURN NULL;
END;
$$;
DROP TRIGGER IF EXISTS mantik_paper_archive_batch_required ON mantik_paper_record_archives;
CREATE CONSTRAINT TRIGGER mantik_paper_archive_batch_required AFTER INSERT ON mantik_paper_record_archives DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION mantik_require_paper_archive_batch();

-- New paper custody cuts bind the actual logical owner epoch. A running
-- predecessor cannot label a post-archive cut as the old epoch-zero format.
CREATE OR REPLACE FUNCTION mantik_require_paper_custody_epoch() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE epoch bigint; body jsonb;
BEGIN
    SELECT coalesce(max(sequence),0) INTO epoch FROM mantik_paper_record_archives;
    body := convert_from(NEW.header,'UTF8')::jsonb;
    IF coalesce((body->>'paper_archive_sequence')::bigint,-1)<>epoch OR
       epoch>0 AND body->>'schema'<>'mantik-paper-feed-checkpoint-v2' THEN
        RAISE EXCEPTION 'paper custody archive epoch missing or changed' USING ERRCODE='23514';
    END IF;
    RETURN NEW;
END;
$$;
DROP TRIGGER IF EXISTS mantik_paper_custody_epoch_required ON mantik_paper_feed_checkpoints;
CREATE TRIGGER mantik_paper_custody_epoch_required BEFORE INSERT ON mantik_paper_feed_checkpoints FOR EACH ROW EXECUTE FUNCTION mantik_require_paper_custody_epoch();

COMMENT ON TABLE mantik_paper_record_archives IS 'Original retained paper owners and replay context; creation epochs prepare bounded historical membership; active originals remain retained';
