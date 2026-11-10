-- Selection metadata includes decisions whose effects occur outside their
-- original comparison command. Native segment replay remains the authority.
CREATE OR REPLACE FUNCTION mantik_allocation_financial_decisions(body jsonb)
RETURNS TABLE(decision_id text) LANGUAGE sql IMMUTABLE STRICT AS $$
    WITH effects AS (
        SELECT e->'command'->'effect' AS effect
        FROM jsonb_array_elements(coalesce(nullif(body#>'{record,expirations}','null'::jsonb),'[]'::jsonb)) e
        UNION ALL
        SELECT e->'command'->'effect'
        FROM jsonb_array_elements(coalesce(nullif(body#>'{record,executions}','null'::jsonb),'[]'::jsonb)) a
        CROSS JOIN LATERAL jsonb_array_elements(coalesce(nullif(a->'entries','null'::jsonb),'[]'::jsonb)) e
        UNION ALL
        SELECT e->'command'->'effect'
        FROM jsonb_array_elements(coalesce(nullif(body#>'{execution,executions}','null'::jsonb),'[]'::jsonb)) a
        CROSS JOIN LATERAL jsonb_array_elements(coalesce(nullif(a->'entries','null'::jsonb),'[]'::jsonb)) e
    ), selected AS (
        SELECT p->>'id' AS id
        FROM jsonb_array_elements(coalesce(nullif(body#>'{record,command,decisions}','null'::jsonb),'[]'::jsonb)) p
        UNION
        SELECT effect->>'decision_id' FROM effects
    )
    SELECT id FROM selected WHERE id IS NOT NULL ORDER BY id COLLATE "C";
$$;

CREATE TABLE IF NOT EXISTS mantik_owned_allocation_financial_refs (
    sequence integer NOT NULL REFERENCES mantik_owned_allocation_members(sequence),
    decision_id text NOT NULL CHECK(decision_id ~ '^[a-zA-Z0-9_-]{1,128}$'),
    PRIMARY KEY(sequence,decision_id)
);
CREATE INDEX IF NOT EXISTS mantik_owned_allocation_financial_history
ON mantik_owned_allocation_financial_refs(decision_id COLLATE "C",sequence);

CREATE OR REPLACE FUNCTION mantik_check_allocation_financial_ref() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE raw bytea; expected_sha text;
BEGIN
    SELECT a.payload,m.item_sha256 INTO raw,expected_sha
    FROM mantik_owned_allocation_members m JOIN mantik_owned_allocation_archive a USING(sequence)
    WHERE m.sequence=NEW.sequence;
    IF NOT FOUND OR encode(sha256(raw),'hex') IS DISTINCT FROM expected_sha OR
       NOT EXISTS(SELECT 1 FROM mantik_allocation_financial_decisions(convert_from(raw,'UTF8')::jsonb) d
                  WHERE d.decision_id=NEW.decision_id) THEN
        RAISE EXCEPTION 'allocation financial reference source mismatch' USING ERRCODE='23514';
    END IF;
    RETURN NEW;
END;
$$;
DROP TRIGGER IF EXISTS mantik_allocation_financial_ref_owner ON mantik_owned_allocation_financial_refs;
CREATE TRIGGER mantik_allocation_financial_ref_owner BEFORE INSERT ON mantik_owned_allocation_financial_refs
FOR EACH ROW EXECUTE FUNCTION mantik_check_allocation_financial_ref();
DROP TRIGGER IF EXISTS mantik_immutable_allocation_financial_refs ON mantik_owned_allocation_financial_refs;
CREATE TRIGGER mantik_immutable_allocation_financial_refs BEFORE UPDATE OR DELETE ON mantik_owned_allocation_financial_refs
FOR EACH ROW EXECUTE FUNCTION mantik_immutable_paper_archive();

-- Derive references only from existing indexed original payloads. This does
-- not reconstruct missing original checkpoints or attest native evaluations.
DO $$ BEGIN
    IF EXISTS(SELECT 1 FROM mantik_owned_allocation_members m
              LEFT JOIN mantik_owned_allocation_archive a USING(sequence)
              WHERE a.sequence IS NULL OR m.command_id IS DISTINCT FROM a.command_id OR
                    m.item_sha256 IS DISTINCT FROM encode(sha256(a.payload),'hex')) THEN
        RAISE EXCEPTION 'allocation financial backfill has damaged originals' USING ERRCODE='23514';
    END IF;
END $$;
INSERT INTO mantik_owned_allocation_financial_refs(sequence,decision_id)
SELECT m.sequence,d.decision_id
FROM mantik_owned_allocation_members m JOIN mantik_owned_allocation_archive a USING(sequence)
CROSS JOIN LATERAL mantik_allocation_financial_decisions(convert_from(a.payload,'UTF8')::jsonb) d
ON CONFLICT DO NOTHING;

CREATE OR REPLACE FUNCTION mantik_require_allocation_financial_refs() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE raw bytea; expected text[]; actual text[];
BEGIN
    SELECT payload INTO raw FROM mantik_owned_allocation_archive WHERE sequence=NEW.sequence;
    SELECT array_agg(decision_id ORDER BY decision_id COLLATE "C") INTO expected
      FROM mantik_allocation_financial_decisions(convert_from(raw,'UTF8')::jsonb);
    SELECT array_agg(decision_id ORDER BY decision_id COLLATE "C") INTO actual
      FROM mantik_owned_allocation_financial_refs WHERE sequence=NEW.sequence;
    IF expected IS DISTINCT FROM actual THEN
        RAISE EXCEPTION 'allocation financial references incomplete' USING ERRCODE='23514';
    END IF;
    RETURN NULL;
END;
$$;
DROP TRIGGER IF EXISTS mantik_allocation_financial_refs_complete ON mantik_owned_allocation_members;
CREATE CONSTRAINT TRIGGER mantik_allocation_financial_refs_complete
AFTER INSERT ON mantik_owned_allocation_members DEFERRABLE INITIALLY DEFERRED
FOR EACH ROW EXECUTE FUNCTION mantik_require_allocation_financial_refs();

COMMENT ON TABLE mantik_owned_allocation_financial_refs IS
'Original comparison and financial-effect decision references; immutable selection metadata, not native or qualification proof';
