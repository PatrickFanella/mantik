CREATE TABLE IF NOT EXISTS mantik_paper_feed_checkpoints (
    sequence bigint PRIMARY KEY CHECK(sequence>0),
    archive_sequence bigint NOT NULL REFERENCES mantik_feed_archives(sequence),
    cut_seconds bigint NOT NULL,
    cut_nanos integer NOT NULL CHECK(cut_nanos BETWEEN 0 AND 999999999),
    source_sha256 text NOT NULL UNIQUE CHECK(source_sha256 ~ '^[a-f0-9]{64}$'),
    sha256 text NOT NULL UNIQUE CHECK(sha256 ~ '^[a-f0-9]{64}$'),
    header bytea NOT NULL CHECK(octet_length(header)<=4096),
    payload bytea NOT NULL CHECK(octet_length(payload)<=1048576)
);
COMMENT ON TABLE mantik_paper_feed_checkpoints IS 'Source-backed custody of retained paper decision/outcome bases at a feed archive cut; no pruning, admission or execution authority';

-- Older running paper writers must honor a newly sealed cut too. UTC seconds
-- and a padded fractional part retain nanoseconds without timestamp rounding.
CREATE OR REPLACE FUNCTION mantik_fence_paper_feed_cut() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE
    cut_s bigint; cut_n integer; event_time text; event_s bigint; event_n integer;
BEGIN
    SELECT cut_seconds,cut_nanos INTO cut_s,cut_n FROM mantik_paper_feed_checkpoints ORDER BY sequence DESC LIMIT 1;
    IF NOT FOUND THEN RETURN NEW; END IF;
    event_time := convert_from(NEW.payload,'UTF8')::jsonb #>> ARRAY['record',TG_ARGV[0]];
    IF event_time IS NULL OR event_time !~ '^[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}:[0-9]{2}:[0-9]{2}([.][0-9]{1,9})?Z$' THEN
        RAISE EXCEPTION 'invalid paper checkpoint event clock' USING ERRCODE='23514';
    END IF;
    event_s := extract(epoch FROM regexp_replace(event_time,'[.][0-9]+Z$','Z')::timestamptz)::bigint;
    event_n := rpad(coalesce(substring(event_time FROM '[.]([0-9]{1,9})Z$'),'0'),9,'0')::integer;
    IF (event_s,event_n)<=(cut_s,cut_n) THEN
        RAISE EXCEPTION 'paper source checkpoint clock moved backwards' USING ERRCODE='23514';
    END IF;
    RETURN NEW;
END;
$$;
DROP TRIGGER IF EXISTS mantik_paper_decision_feed_cut ON mantik_paper_decisions;
CREATE TRIGGER mantik_paper_decision_feed_cut BEFORE INSERT ON mantik_paper_decisions FOR EACH ROW EXECUTE FUNCTION mantik_fence_paper_feed_cut('finalized_at');
DROP TRIGGER IF EXISTS mantik_paper_outcome_feed_cut ON mantik_paper_outcomes;
CREATE TRIGGER mantik_paper_outcome_feed_cut BEFORE INSERT ON mantik_paper_outcomes FOR EACH ROW EXECUTE FUNCTION mantik_fence_paper_feed_cut('evaluated_at');
