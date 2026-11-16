CREATE OR REPLACE FUNCTION mantik_strategy_batch_immutable() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 RAISE EXCEPTION 'strategy batch accounting is append-only' USING ERRCODE='23514';
END;
$$;
-- Fence before a model call can capture generated output, even if reply
-- persistence fails. No guessed trial count is stored on the intent.
CREATE TABLE IF NOT EXISTS mantik_strategy_trial_batch_intents (
 id text PRIMARY KEY,
 family text NOT NULL
);
CREATE INDEX IF NOT EXISTS mantik_strategy_trial_batch_intents_family ON mantik_strategy_trial_batch_intents(family);
-- A reply intent is immutable; reconciliation is an append-only receipt.
CREATE TABLE IF NOT EXISTS mantik_strategy_trial_batches (
 id text PRIMARY KEY,
 family text NOT NULL,
 payload json NOT NULL CHECK (json_typeof(payload)='object' AND payload->>'id'=id AND payload->>'family'=family AND octet_length(payload::text)<=8388608)
);
CREATE INDEX IF NOT EXISTS mantik_strategy_trial_batches_family ON mantik_strategy_trial_batches(family);
CREATE TABLE IF NOT EXISTS mantik_strategy_trial_batch_resolutions (
 id text PRIMARY KEY REFERENCES mantik_strategy_trial_batches(id)
);
DROP TRIGGER IF EXISTS strategy_batch_immutable ON mantik_strategy_trial_batches;
CREATE TRIGGER strategy_batch_immutable BEFORE UPDATE OR DELETE OR TRUNCATE ON mantik_strategy_trial_batches FOR EACH STATEMENT EXECUTE FUNCTION mantik_strategy_batch_immutable();
DROP TRIGGER IF EXISTS strategy_batch_resolution_immutable ON mantik_strategy_trial_batch_resolutions;
CREATE TRIGGER strategy_batch_resolution_immutable BEFORE UPDATE OR DELETE OR TRUNCATE ON mantik_strategy_trial_batch_resolutions FOR EACH STATEMENT EXECUTE FUNCTION mantik_strategy_batch_immutable();
DROP TRIGGER IF EXISTS strategy_batch_intent_immutable ON mantik_strategy_trial_batch_intents;
CREATE TRIGGER strategy_batch_intent_immutable BEFORE UPDATE OR DELETE OR TRUNCATE ON mantik_strategy_trial_batch_intents FOR EACH STATEMENT EXECUTE FUNCTION mantik_strategy_batch_immutable();
