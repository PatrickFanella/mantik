-- Separate typed boundary; legacy threshold bytes and schema dispatch stay intact.
CREATE TABLE IF NOT EXISTS mantik_forward_declarations (
 id text PRIMARY KEY,
 family text NOT NULL,
 payload json NOT NULL CHECK (octet_length(payload::text)<=2097152 AND json_typeof(payload)='object' AND payload->'declaration'->>'id'=id AND payload->'declaration'->>'family'=family)
);
CREATE OR REPLACE FUNCTION mantik_forward_immutable() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 RAISE EXCEPTION 'forward declarations are append-only' USING ERRCODE='23514';
END;
$$;
DROP TRIGGER IF EXISTS forward_immutable ON mantik_forward_declarations;
CREATE TRIGGER forward_immutable BEFORE UPDATE OR DELETE OR TRUNCATE ON mantik_forward_declarations FOR EACH STATEMENT EXECUTE FUNCTION mantik_forward_immutable();
