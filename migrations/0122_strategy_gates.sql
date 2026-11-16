CREATE TABLE IF NOT EXISTS mantik_strategy_gates (
 id text PRIMARY KEY,
 declaration json NOT NULL CHECK (json_typeof(declaration)='object' AND declaration->>'ID'=id),
 declared_at timestamptz NOT NULL DEFAULT clock_timestamp()
);
CREATE TABLE IF NOT EXISTS mantik_strategy_gate_decisions (
 id text PRIMARY KEY REFERENCES mantik_strategy_gates(id),
 decision json NOT NULL CHECK (json_typeof(decision)='object' AND decision->>'ID'=id)
);
CREATE OR REPLACE FUNCTION mantik_strategy_gate_immutable() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 RAISE EXCEPTION 'strategy gates are append-only' USING ERRCODE='23514';
END;
$$;
DROP TRIGGER IF EXISTS strategy_gate_immutable ON mantik_strategy_gates;
CREATE TRIGGER strategy_gate_immutable BEFORE UPDATE OR DELETE ON mantik_strategy_gates FOR EACH ROW EXECUTE FUNCTION mantik_strategy_gate_immutable();
DROP TRIGGER IF EXISTS strategy_gate_decision_immutable ON mantik_strategy_gate_decisions;
CREATE TRIGGER strategy_gate_decision_immutable BEFORE UPDATE OR DELETE ON mantik_strategy_gate_decisions FOR EACH ROW EXECUTE FUNCTION mantik_strategy_gate_immutable();
DROP TRIGGER IF EXISTS strategy_gate_no_truncate ON mantik_strategy_gates;
CREATE TRIGGER strategy_gate_no_truncate BEFORE TRUNCATE ON mantik_strategy_gates FOR EACH STATEMENT EXECUTE FUNCTION mantik_strategy_gate_immutable();
DROP TRIGGER IF EXISTS strategy_gate_decision_no_truncate ON mantik_strategy_gate_decisions;
CREATE TRIGGER strategy_gate_decision_no_truncate BEFORE TRUNCATE ON mantik_strategy_gate_decisions FOR EACH STATEMENT EXECUTE FUNCTION mantik_strategy_gate_immutable();
