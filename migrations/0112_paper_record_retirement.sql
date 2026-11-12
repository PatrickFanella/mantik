-- Retirement owner: a decision or outcome leaves the active working set only
-- at the current paper archive epoch, only when that archive seals its exact
-- creation pin, and only before a custody checkpoint binds that epoch. The
-- active row must already be removed in the same transaction, so a retired
-- owner never remains in a working table. The creation pin stays immutable;
-- epoch rows are never deleted.
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
    IF OLD.kind NOT IN ('decision','outcome') OR NEW.retired_epoch<>head OR coalesce(bound,0)>=head OR
       OLD.kind='decision' AND EXISTS(SELECT 1 FROM mantik_paper_decisions WHERE id=OLD.record_id) OR
       OLD.kind='outcome' AND EXISTS(SELECT 1 FROM mantik_paper_outcomes WHERE id=OLD.record_id) OR
       NOT EXISTS(SELECT 1 FROM mantik_paper_archive_members m
                  WHERE m.archive_sequence=head AND m.kind=OLD.kind AND m.record_id=OLD.record_id AND
                        m.created_epoch=OLD.created_epoch AND m.record_sha256=OLD.record_sha256) THEN
        RAISE EXCEPTION 'paper record retirement is not sealed by the current archive epoch' USING ERRCODE='23514';
    END IF;
    RETURN NEW;
END;
$$;
DROP TRIGGER IF EXISTS mantik_immutable_paper_epochs ON mantik_paper_record_epochs;
CREATE TRIGGER mantik_immutable_paper_epochs BEFORE UPDATE OR DELETE ON mantik_paper_record_epochs FOR EACH ROW EXECUTE FUNCTION mantik_retire_paper_epoch();

COMMENT ON COLUMN mantik_paper_record_epochs.retired_epoch IS
    'Exclusive end of active ownership; set once by the retirement owner at the sealing archive epoch; original creation pins remain unchanged';
