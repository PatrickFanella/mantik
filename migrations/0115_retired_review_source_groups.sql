-- A source claim group is reviewed once per decision. When a source-bound
-- review retires, its group stays reserved here so a later command cannot
-- review the same group and decision again. A row needs a retired review and
-- never changes.
CREATE TABLE IF NOT EXISTS mantik_retired_review_source_groups (
    source_group text PRIMARY KEY CHECK (source_group ~ '^(social|copy):[a-f0-9]{64}:[a-f0-9]{64}$'),
    review_id text NOT NULL UNIQUE
);
CREATE OR REPLACE FUNCTION mantik_retired_review_source_group() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF TG_OP<>'INSERT' THEN
        RAISE EXCEPTION 'retired review source group is immutable' USING ERRCODE='23514';
    END IF;
    IF NOT EXISTS(SELECT 1 FROM mantik_paper_record_epochs
                  WHERE kind='review' AND record_id=NEW.review_id AND retired_epoch IS NOT NULL) THEN
        RAISE EXCEPTION 'review source group belongs to an active review' USING ERRCODE='23514';
    END IF;
    RETURN NEW;
END;
$$;
DROP TRIGGER IF EXISTS mantik_retired_review_source_group ON mantik_retired_review_source_groups;
CREATE TRIGGER mantik_retired_review_source_group BEFORE INSERT OR UPDATE OR DELETE ON mantik_retired_review_source_groups
FOR EACH ROW EXECUTE FUNCTION mantik_retired_review_source_group();
