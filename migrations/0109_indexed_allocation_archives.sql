-- Preserve original checkpoints and bounded sealed segments. Existing journal
-- and archive bytes stay unchanged; earlier unindexed history needs its native
-- full-history audit before an indexed read can be used.
CREATE TABLE IF NOT EXISTS mantik_owned_allocation_checkpoints (
    sequence integer PRIMARY KEY CHECK(sequence>0),
    sha256 text NOT NULL CHECK(sha256 ~ '^[a-f0-9]{64}$'),
    payload bytea NOT NULL CHECK(octet_length(payload)<=67108864)
);
CREATE TABLE IF NOT EXISTS mantik_owned_allocation_batches (
    checkpoint_sequence integer PRIMARY KEY REFERENCES mantik_owned_allocation_checkpoints(sequence),
    first_sequence integer NOT NULL CHECK(first_sequence>0 AND first_sequence<=checkpoint_sequence),
    members_sha256 text NOT NULL CHECK(members_sha256 ~ '^[a-f0-9]{64}$')
);
CREATE INDEX IF NOT EXISTS mantik_owned_allocation_batch_start ON mantik_owned_allocation_batches(first_sequence);
CREATE TABLE IF NOT EXISTS mantik_owned_allocation_members (
    sequence integer PRIMARY KEY CHECK(sequence>0),
    checkpoint_sequence integer NOT NULL REFERENCES mantik_owned_allocation_batches(checkpoint_sequence),
    command_id text NOT NULL UNIQUE,
    kind text NOT NULL CHECK(kind IN ('comparison','execution')),
    record_sha256 text NOT NULL CHECK(record_sha256 ~ '^[a-f0-9]{64}$'),
    item_sha256 text NOT NULL CHECK(item_sha256 ~ '^[a-f0-9]{64}$')
);
CREATE INDEX IF NOT EXISTS mantik_owned_allocation_member_batch ON mantik_owned_allocation_members(checkpoint_sequence,sequence);
CREATE TABLE IF NOT EXISTS mantik_owned_allocation_decision_refs (
    sequence integer NOT NULL REFERENCES mantik_owned_allocation_members(sequence),
    decision_id text NOT NULL,
    decision_sha256 text NOT NULL CHECK(decision_sha256 ~ '^[a-f0-9]{64}$'),
    PRIMARY KEY(sequence,decision_id)
);
CREATE INDEX IF NOT EXISTS mantik_owned_allocation_decision_history ON mantik_owned_allocation_decision_refs(decision_id COLLATE "C",sequence);

CREATE OR REPLACE FUNCTION mantik_check_owned_allocation_member() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE raw bytea; command text; first integer; last integer; body jsonb; record jsonb; execution boolean;
BEGIN
    SELECT a.payload,a.command_id,b.first_sequence,b.checkpoint_sequence INTO raw,command,first,last
    FROM mantik_owned_allocation_archive a CROSS JOIN mantik_owned_allocation_batches b
    WHERE a.sequence=NEW.sequence AND b.checkpoint_sequence=NEW.checkpoint_sequence;
    IF NOT FOUND OR NEW.sequence<first OR NEW.sequence>last THEN
        RAISE EXCEPTION 'owned allocation member has no sealed owner' USING ERRCODE='23514';
    END IF;
    body := convert_from(raw,'UTF8')::jsonb;
    execution := body->'execution' IS NOT NULL AND body->'execution'<>'null'::jsonb;
    record := CASE WHEN execution THEN body->'execution' ELSE body->'record' END;
    IF NEW.command_id IS DISTINCT FROM command OR NEW.command_id IS DISTINCT FROM record->'command'->>'id' OR
       NEW.record_sha256 IS DISTINCT FROM record->>'sha256' OR NEW.item_sha256 IS DISTINCT FROM encode(sha256(raw),'hex') OR
       (NEW.kind='execution') IS DISTINCT FROM execution THEN
        RAISE EXCEPTION 'owned allocation member source mismatch' USING ERRCODE='23514';
    END IF;
    RETURN NEW;
END;
$$;
DROP TRIGGER IF EXISTS mantik_owned_allocation_member_owner ON mantik_owned_allocation_members;
CREATE TRIGGER mantik_owned_allocation_member_owner BEFORE INSERT ON mantik_owned_allocation_members
FOR EACH ROW EXECUTE FUNCTION mantik_check_owned_allocation_member();

CREATE OR REPLACE FUNCTION mantik_check_owned_allocation_ref() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF NOT EXISTS(SELECT 1 FROM mantik_owned_allocation_members m
        JOIN mantik_owned_allocation_archive a USING(sequence)
        CROSS JOIN LATERAL jsonb_array_elements(convert_from(a.payload,'UTF8')::jsonb->'record'->'command'->'decisions') p
        WHERE m.sequence=NEW.sequence AND m.kind='comparison' AND p->>'id'=NEW.decision_id AND p->>'sha256'=NEW.decision_sha256) THEN
        RAISE EXCEPTION 'owned allocation reference source mismatch' USING ERRCODE='23514';
    END IF;
    RETURN NEW;
END;
$$;
DROP TRIGGER IF EXISTS mantik_owned_allocation_ref_owner ON mantik_owned_allocation_decision_refs;
CREATE TRIGGER mantik_owned_allocation_ref_owner BEFORE INSERT ON mantik_owned_allocation_decision_refs
FOR EACH ROW EXECUTE FUNCTION mantik_check_owned_allocation_ref();

CREATE OR REPLACE FUNCTION mantik_check_owned_allocation_batch() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE checkpoint jsonb; records integer;
BEGIN
    SELECT convert_from(payload,'UTF8')::jsonb INTO checkpoint
      FROM mantik_owned_allocation_checkpoints WHERE sequence=NEW.checkpoint_sequence;
    SELECT count(*) INTO records FROM mantik_owned_allocation_members WHERE checkpoint_sequence=NEW.checkpoint_sequence;
    IF (checkpoint->>'sequence')::integer IS DISTINCT FROM NEW.checkpoint_sequence OR
       jsonb_array_length(checkpoint->'seals') IS NULL OR jsonb_array_length(checkpoint->'seals')<1 OR
       (checkpoint->'seals'->>-1)::integer IS DISTINCT FROM NEW.checkpoint_sequence OR
       NEW.first_sequence IS DISTINCT FROM (CASE WHEN jsonb_array_length(checkpoint->'seals')>1
           THEN (checkpoint->'seals'->>-2)::integer+1 ELSE 1 END) OR
       records<>NEW.checkpoint_sequence-NEW.first_sequence+1 OR
       EXISTS(SELECT 1 FROM mantik_owned_allocation_members m
              LEFT JOIN mantik_owned_allocation_archive a USING(sequence)
              CROSS JOIN LATERAL (SELECT convert_from(a.payload,'UTF8')::jsonb AS body) b
              CROSS JOIN LATERAL (SELECT CASE WHEN b.body->'execution' IS NOT NULL AND b.body->'execution'<>'null'::jsonb
                                           THEN b.body->'execution' ELSE b.body->'record' END AS record) r
              WHERE m.checkpoint_sequence=NEW.checkpoint_sequence AND
                (a.sequence IS NULL OR m.sequence<NEW.first_sequence OR m.sequence>NEW.checkpoint_sequence OR
                 m.command_id IS DISTINCT FROM a.command_id OR m.command_id IS DISTINCT FROM r.record->'command'->>'id' OR
                 m.record_sha256 IS DISTINCT FROM r.record->>'sha256' OR m.item_sha256<>encode(sha256(a.payload),'hex') OR
                 (m.kind='execution')<>(b.body->'execution' IS NOT NULL AND b.body->'execution'<>'null'::jsonb) OR
                 (SELECT count(*) FROM mantik_owned_allocation_decision_refs d WHERE d.sequence=m.sequence) IS DISTINCT FROM
                   CASE WHEN m.kind='execution' THEN 0 ELSE jsonb_array_length(r.record->'command'->'decisions') END OR
                 EXISTS(SELECT 1 FROM mantik_owned_allocation_decision_refs d WHERE d.sequence=m.sequence AND
                     NOT EXISTS(SELECT 1 FROM jsonb_array_elements(r.record->'command'->'decisions') p
                                WHERE p->>'id'=d.decision_id AND p->>'sha256'=d.decision_sha256)))) THEN
        RAISE EXCEPTION 'owned allocation archive batch mismatch' USING ERRCODE='23514';
    END IF;
    RETURN NULL;
END;
$$;
DROP TRIGGER IF EXISTS mantik_owned_allocation_batch_complete ON mantik_owned_allocation_batches;
CREATE CONSTRAINT TRIGGER mantik_owned_allocation_batch_complete
AFTER INSERT ON mantik_owned_allocation_batches DEFERRABLE INITIALLY DEFERRED
FOR EACH ROW EXECUTE FUNCTION mantik_check_owned_allocation_batch();

DROP TRIGGER IF EXISTS mantik_immutable_allocation_checkpoints ON mantik_owned_allocation_checkpoints;
CREATE TRIGGER mantik_immutable_allocation_checkpoints BEFORE UPDATE OR DELETE ON mantik_owned_allocation_checkpoints
FOR EACH ROW EXECUTE FUNCTION mantik_immutable_paper_archive();
DROP TRIGGER IF EXISTS mantik_immutable_allocation_batches ON mantik_owned_allocation_batches;
CREATE TRIGGER mantik_immutable_allocation_batches BEFORE UPDATE OR DELETE ON mantik_owned_allocation_batches
FOR EACH ROW EXECUTE FUNCTION mantik_immutable_paper_archive();
DROP TRIGGER IF EXISTS mantik_immutable_allocation_members ON mantik_owned_allocation_members;
CREATE TRIGGER mantik_immutable_allocation_members BEFORE UPDATE OR DELETE ON mantik_owned_allocation_members
FOR EACH ROW EXECUTE FUNCTION mantik_immutable_paper_archive();
DROP TRIGGER IF EXISTS mantik_immutable_allocation_refs ON mantik_owned_allocation_decision_refs;
CREATE TRIGGER mantik_immutable_allocation_refs BEFORE UPDATE OR DELETE ON mantik_owned_allocation_decision_refs
FOR EACH ROW EXECUTE FUNCTION mantik_immutable_paper_archive();

-- A running predecessor may still append active records, but cannot seal a
-- checkpoint without committing its complete original segment index.
CREATE OR REPLACE FUNCTION mantik_require_owned_allocation_index() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE current_checkpoint jsonb; previous_checkpoint jsonb;
BEGIN
    current_checkpoint := convert_from(NEW.payload,'UTF8')::jsonb->'checkpoint';
    IF TG_OP='UPDATE' THEN
        previous_checkpoint := convert_from(OLD.payload,'UTF8')::jsonb->'checkpoint';
        IF current_checkpoint=previous_checkpoint THEN RETURN NULL; END IF;
        IF previous_checkpoint IS NOT NULL AND previous_checkpoint<>'null'::jsonb AND
           (current_checkpoint IS NULL OR current_checkpoint='null'::jsonb OR
            (current_checkpoint->>'sequence')::integer<=(previous_checkpoint->>'sequence')::integer) THEN
            RAISE EXCEPTION 'owned allocation checkpoint cannot be removed or replaced' USING ERRCODE='23514';
        END IF;
    END IF;
    IF current_checkpoint IS NULL OR current_checkpoint='null'::jsonb THEN RETURN NULL; END IF;
    IF NOT EXISTS(SELECT 1 FROM mantik_owned_allocation_checkpoints c
        JOIN mantik_owned_allocation_batches b ON b.checkpoint_sequence=c.sequence
        WHERE c.sequence=(current_checkpoint->>'sequence')::integer AND
              c.sha256=current_checkpoint->>'sha256' AND convert_from(c.payload,'UTF8')::jsonb=current_checkpoint) THEN
        RAISE EXCEPTION 'owned allocation checkpoint requires original segment index' USING ERRCODE='23514';
    END IF;
    RETURN NULL;
END;
$$;
DROP TRIGGER IF EXISTS mantik_owned_allocation_index_required ON mantik_owned_allocations;
CREATE CONSTRAINT TRIGGER mantik_owned_allocation_index_required
AFTER INSERT OR UPDATE ON mantik_owned_allocations DEFERRABLE INITIALLY DEFERRED
FOR EACH ROW EXECUTE FUNCTION mantik_require_owned_allocation_index();

COMMENT ON TABLE mantik_owned_allocation_checkpoints IS 'Original allocation checkpoint envelopes; full native audit remains the authority for replay';
COMMENT ON TABLE mantik_owned_allocation_batches IS 'Immutable complete membership for newly sealed allocation segments; no backfill or qualification claim';
