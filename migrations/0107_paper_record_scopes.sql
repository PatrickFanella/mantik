-- These fields are owner-derived selection metadata, separate from the frozen
-- creation pins already hashed into v1 archives. No payload or pin is rewritten.
LOCK TABLE mantik_paper_decisions,mantik_paper_outcomes IN SHARE ROW EXCLUSIVE MODE;
CREATE OR REPLACE FUNCTION mantik_paper_record_scope(kind text, id text, payload bytea) RETURNS jsonb LANGUAGE plpgsql AS $$
DECLARE body jsonb; record jsonb; decision jsonb; event_time text; decision_time text;
BEGIN
    IF kind NOT IN ('decision','outcome') THEN RETURN '{}'::jsonb; END IF;
    body := convert_from(payload,'UTF8')::jsonb;
    record := body->'record';
    decision := CASE WHEN kind='decision' THEN record ELSE body->'basis'->'decision' END;
    IF record->'command'->>'id' IS DISTINCT FROM id OR
       kind='outcome' AND (record->'command'->>'decision_id' IS DISTINCT FROM decision->'command'->>'id' OR
                          record->'command'->>'decision_sha256' IS DISTINCT FROM decision->>'sha256') THEN
        RAISE EXCEPTION 'paper scope owner mismatch' USING ERRCODE='23514';
    END IF;
    event_time := record->>(CASE WHEN kind='decision' THEN 'finalized_at' ELSE 'evaluated_at' END);
    decision_time := decision->>'finalized_at';
    IF event_time IS NULL OR decision_time IS NULL OR
       event_time !~ '^[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}:[0-9]{2}:[0-9]{2}([.][0-9]{1,9})?Z$' OR
       decision_time !~ '^[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}:[0-9]{2}:[0-9]{2}([.][0-9]{1,9})?Z$' THEN
        RAISE EXCEPTION 'invalid paper scope clock' USING ERRCODE='23514';
    END IF;
    RETURN jsonb_build_object(
        'record_sha256',record->>'sha256',
        'record_at_seconds',extract(epoch FROM regexp_replace(event_time,'[.][0-9]+Z$','Z')::timestamptz)::bigint,
        'record_at_nanos',rpad(coalesce(substring(event_time FROM '[.]([0-9]{1,9})Z$'),'0'),9,'0')::integer,
        'strategy_id',decision->'command'->>'strategy_id',
        'strategy_sha256',decision->'command'->>'strategy_sha256',
        'decision_id',decision->'command'->>'id',
        'decision_sha256',decision->>'sha256',
        'decision_at_seconds',extract(epoch FROM regexp_replace(decision_time,'[.][0-9]+Z$','Z')::timestamptz)::bigint,
        'decision_at_nanos',rpad(coalesce(substring(decision_time FROM '[.]([0-9]{1,9})Z$'),'0'),9,'0')::integer);
END;
$$;

DO $$
DECLARE had_scope boolean;
BEGIN
    SELECT EXISTS(SELECT 1 FROM pg_attribute WHERE attrelid='mantik_paper_record_epochs'::regclass AND attname='strategy_id' AND NOT attisdropped) INTO had_scope;
    ALTER TABLE mantik_paper_record_epochs
        ADD COLUMN IF NOT EXISTS strategy_id text,
        ADD COLUMN IF NOT EXISTS strategy_sha256 text,
        ADD COLUMN IF NOT EXISTS decision_id text,
        ADD COLUMN IF NOT EXISTS decision_sha256 text,
        ADD COLUMN IF NOT EXISTS decision_at_seconds bigint,
        ADD COLUMN IF NOT EXISTS decision_at_nanos integer;
    IF had_scope AND EXISTS(SELECT 1 FROM mantik_paper_record_epochs WHERE kind IN ('decision','outcome') AND
        (strategy_id IS NULL OR strategy_sha256 IS NULL OR decision_id IS NULL OR decision_sha256 IS NULL OR decision_at_seconds IS NULL OR decision_at_nanos IS NULL)) THEN
        RAISE EXCEPTION 'paper selection scope missing on populated replay' USING ERRCODE='23514';
    END IF;
    -- Backfill only from actual retained owner envelopes and verify the original
    -- hash/clock reservation. A lost owner is never reconstructed by guessing.
    IF EXISTS(SELECT 1 FROM (
        SELECT 'decision' AS kind,id,payload FROM mantik_paper_decisions
        UNION ALL SELECT 'outcome',id,payload FROM mantik_paper_outcomes
    ) a LEFT JOIN mantik_paper_record_epochs e ON e.kind=a.kind AND e.record_id=a.id
      CROSS JOIN LATERAL (SELECT mantik_paper_record_scope(a.kind,a.id,a.payload) AS p) s
      WHERE e.kind IS NULL OR e.record_sha256 IS DISTINCT FROM s.p->>'record_sha256' OR
        e.at_seconds IS DISTINCT FROM (s.p->>'record_at_seconds')::bigint OR e.at_nanos IS DISTINCT FROM (s.p->>'record_at_nanos')::integer OR
        had_scope AND ROW(e.strategy_id,e.strategy_sha256,e.decision_id,e.decision_sha256,e.decision_at_seconds,e.decision_at_nanos)
          IS DISTINCT FROM ROW(s.p->>'strategy_id',s.p->>'strategy_sha256',s.p->>'decision_id',s.p->>'decision_sha256',(s.p->>'decision_at_seconds')::bigint,(s.p->>'decision_at_nanos')::integer)) THEN
        RAISE EXCEPTION 'paper selection scope source conflict' USING ERRCODE='23514';
    END IF;
    IF NOT had_scope THEN
        ALTER TABLE mantik_paper_record_epochs DISABLE TRIGGER mantik_immutable_paper_epochs;
        UPDATE mantik_paper_record_epochs e SET
            strategy_id=s.p->>'strategy_id',strategy_sha256=s.p->>'strategy_sha256',
            decision_id=s.p->>'decision_id',decision_sha256=s.p->>'decision_sha256',
            decision_at_seconds=(s.p->>'decision_at_seconds')::bigint,decision_at_nanos=(s.p->>'decision_at_nanos')::integer
        FROM (SELECT 'decision' AS kind,id,mantik_paper_record_scope('decision',id,payload) AS p FROM mantik_paper_decisions
              UNION ALL SELECT 'outcome',id,mantik_paper_record_scope('outcome',id,payload) FROM mantik_paper_outcomes) s
        WHERE e.kind=s.kind AND e.record_id=s.id;
        ALTER TABLE mantik_paper_record_epochs ENABLE TRIGGER mantik_immutable_paper_epochs;
    END IF;
    IF NOT EXISTS(SELECT 1 FROM pg_constraint WHERE conrelid='mantik_paper_record_epochs'::regclass AND conname='mantik_paper_scope_shape') THEN
        ALTER TABLE mantik_paper_record_epochs ADD CONSTRAINT mantik_paper_scope_shape CHECK (
            CASE WHEN kind IN ('decision','outcome') THEN
                strategy_id IS NOT NULL AND strategy_id<>'' AND strategy_sha256 IS NOT NULL AND strategy_sha256 ~ '^[a-f0-9]{64}$' AND
                decision_id IS NOT NULL AND decision_id<>'' AND decision_sha256 IS NOT NULL AND decision_sha256 ~ '^[a-f0-9]{64}$' AND
                decision_at_seconds IS NOT NULL AND decision_at_nanos IS NOT NULL AND decision_at_nanos BETWEEN 0 AND 999999999
            ELSE strategy_id IS NULL AND strategy_sha256 IS NULL AND decision_id IS NULL AND decision_sha256 IS NULL AND decision_at_seconds IS NULL AND decision_at_nanos IS NULL END);
    END IF;
END;
$$;

CREATE INDEX IF NOT EXISTS mantik_paper_sample_window ON mantik_paper_record_epochs
    (strategy_id,strategy_sha256,decision_at_seconds,decision_at_nanos,decision_id,kind,at_seconds,at_nanos,record_id)
    WHERE kind IN ('decision','outcome');

CREATE OR REPLACE FUNCTION mantik_record_paper_epoch() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE body jsonb; record jsonb; event_time text; epoch bigint; scope jsonb;
BEGIN
    body := convert_from(NEW.payload,'UTF8')::jsonb;
    record := CASE WHEN TG_ARGV[2]='record' THEN body->'record' ELSE body END;
    event_time := record->>TG_ARGV[1];
    IF event_time IS NULL OR event_time !~ '^[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}:[0-9]{2}:[0-9]{2}([.][0-9]{1,9})?Z$' THEN
        RAISE EXCEPTION 'invalid paper owner event clock' USING ERRCODE='23514';
    END IF;
    SELECT coalesce(max(sequence),0) INTO epoch FROM mantik_paper_record_archives;
    scope := mantik_paper_record_scope(TG_ARGV[0],NEW.id,NEW.payload);
    INSERT INTO mantik_paper_record_epochs(kind,record_id,created_epoch,record_sha256,at_seconds,at_nanos,
        strategy_id,strategy_sha256,decision_id,decision_sha256,decision_at_seconds,decision_at_nanos)
    VALUES(TG_ARGV[0],NEW.id,epoch,record->>'sha256',
        extract(epoch FROM regexp_replace(event_time,'[.][0-9]+Z$','Z')::timestamptz)::bigint,
        rpad(coalesce(substring(event_time FROM '[.]([0-9]{1,9})Z$'),'0'),9,'0')::integer,
        scope->>'strategy_id',scope->>'strategy_sha256',scope->>'decision_id',scope->>'decision_sha256',
        (scope->>'decision_at_seconds')::bigint,(scope->>'decision_at_nanos')::integer);
    RETURN NEW;
END;
$$;

COMMENT ON INDEX mantik_paper_sample_window IS 'Owner-only assessment sample metadata across active and archived records; exact decision clocks and strategy versions; no payload or qualification authority';

-- PostgreSQL checks candidate rows before ON CONFLICT. Schema 105's duplicate
-- backfill omits these later columns, so preserve its exact original reservation
-- before the shape check. A new or changed identity cannot borrow this path.
CREATE OR REPLACE FUNCTION mantik_replay_paper_scope() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE original mantik_paper_record_epochs;
BEGIN
    IF NEW.kind IN ('decision','outcome') AND
       NEW.strategy_id IS NULL AND NEW.strategy_sha256 IS NULL AND
       NEW.decision_id IS NULL AND NEW.decision_sha256 IS NULL AND
       NEW.decision_at_seconds IS NULL AND NEW.decision_at_nanos IS NULL THEN
        SELECT * INTO original FROM mantik_paper_record_epochs e WHERE e.kind=NEW.kind AND e.record_id=NEW.record_id;
        IF NOT FOUND OR ROW(original.record_sha256,original.at_seconds,original.at_nanos)
            IS DISTINCT FROM ROW(NEW.record_sha256,NEW.at_seconds,NEW.at_nanos) OR
           original.strategy_id IS NULL OR original.strategy_sha256 IS NULL OR
           original.decision_id IS NULL OR original.decision_sha256 IS NULL OR
           original.decision_at_seconds IS NULL OR original.decision_at_nanos IS NULL THEN
            RAISE EXCEPTION 'paper selection scope missing or changed for predecessor replay' USING ERRCODE='23514';
        END IF;
        NEW.strategy_id := original.strategy_id;
        NEW.strategy_sha256 := original.strategy_sha256;
        NEW.decision_id := original.decision_id;
        NEW.decision_sha256 := original.decision_sha256;
        NEW.decision_at_seconds := original.decision_at_seconds;
        NEW.decision_at_nanos := original.decision_at_nanos;
    END IF;
    RETURN NEW;
END;
$$;
DROP TRIGGER IF EXISTS mantik_paper_scope_replay ON mantik_paper_record_epochs;
CREATE TRIGGER mantik_paper_scope_replay BEFORE INSERT ON mantik_paper_record_epochs FOR EACH ROW EXECUTE FUNCTION mantik_replay_paper_scope();
