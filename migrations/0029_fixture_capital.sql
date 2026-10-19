-- Store replay material separately; the aggregate pin fences older writers.
CREATE TABLE mantik_fixture_capital (
 id TEXT PRIMARY KEY REFERENCES mantik_state(id) CHECK(id='demo'),
 account_id TEXT UNIQUE NOT NULL REFERENCES mantik_paper_accounts(id),
 payload JSONB NOT NULL,
 CHECK (COALESCE(jsonb_typeof(payload)='object'
 AND payload->>'schema'='mantik-fixture-capital-checkpoint-v1'
 AND jsonb_typeof(payload->'transitions')='array'
 AND jsonb_array_length(payload->'transitions')<=1024
 AND payload->>'account_sha256' ~ '^[a-f0-9]{64}$'
 AND payload->>'result_sha256' ~ '^[a-f0-9]{64}$'
 AND payload->>'sha256' ~ '^[a-f0-9]{64}$',false)),
 CHECK (octet_length(payload::text)<=4194304)
);
CREATE OR REPLACE FUNCTION mantik_keep_fixture_capital_binding() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF OLD.payload->'fixture_capital_binding' IS NOT NULL
 AND NEW.payload->'fixture_capital_binding' IS DISTINCT FROM OLD.payload->'fixture_capital_binding' THEN
  RAISE EXCEPTION 'fixture capital binding is immutable';
 END IF;
 RETURN NEW;
END $$;
DROP TRIGGER IF EXISTS mantik_keep_fixture_capital_binding ON mantik_state;
CREATE TRIGGER mantik_keep_fixture_capital_binding BEFORE UPDATE ON mantik_state
 FOR EACH ROW EXECUTE FUNCTION mantik_keep_fixture_capital_binding();
