-- Claim reviews retire under the same guard as decisions and outcomes: only at
-- the current paper archive epoch, sealed there with their creation pin, with
-- the active row already removed, and before a custody checkpoint binds the
-- epoch. Assessments stay active.
CREATE OR REPLACE FUNCTION mantik_retire_paper_epoch() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE head bigint; bound bigint;
BEGIN
    IF TG_OP='DELETE' THEN
        RAISE EXCEPTION 'paper archive owner record is immutable' USING ERRCODE='23514';
    END IF;
    IF (to_jsonb(NEW)-'retired_epoch') IS DISTINCT FROM (to_jsonb(OLD)-'retired_epoch') OR
       OLD.retired_epoch IS NOT NULL OR NEW.retired_epoch IS NULL THEN
        RAISE EXCEPTION 'paper archive owner record is immutable' USING ERRCODE='23514';
    END IF;
    SELECT coalesce(max(sequence),0) INTO head FROM mantik_paper_record_archives;
    SELECT (convert_from(header,'UTF8')::jsonb->>'paper_archive_sequence')::bigint INTO bound
    FROM mantik_paper_feed_checkpoints ORDER BY sequence DESC LIMIT 1;
    IF OLD.kind NOT IN ('decision','outcome','review') OR NEW.retired_epoch<>head OR coalesce(bound,0)>=head OR
       OLD.kind='decision' AND EXISTS(SELECT 1 FROM mantik_paper_decisions WHERE id=OLD.record_id) OR
       OLD.kind='outcome' AND EXISTS(SELECT 1 FROM mantik_paper_outcomes WHERE id=OLD.record_id) OR
       OLD.kind='review' AND EXISTS(SELECT 1 FROM mantik_decision_reviews WHERE id=OLD.record_id) OR
       NOT EXISTS(SELECT 1 FROM mantik_paper_archive_members m
                  WHERE m.archive_sequence=head AND m.kind=OLD.kind AND m.record_id=OLD.record_id AND
                        m.created_epoch=OLD.created_epoch AND m.record_sha256=OLD.record_sha256) THEN
        RAISE EXCEPTION 'paper record retirement is not sealed by the current archive epoch' USING ERRCODE='23514';
    END IF;
    RETURN NEW;
END;
$$;
