-- Daily-engine research trials: a durable start, session returns and one
-- terminal outcome. Counting starts includes unfinished/crashed attempts.
CREATE TABLE IF NOT EXISTS mantik_strategy_trials (
    id text PRIMARY KEY CHECK (id ~ '^[a-zA-Z0-9][a-zA-Z0-9._-]{0,127}$'),
    family text NOT NULL CHECK (family ~ '^[a-z][a-z0-9_-]{0,63}$'),
    started_at timestamptz NOT NULL,
    -- json preserves exact numeric spelling for idempotent declaration retries.
    payload json NOT NULL CHECK (json_typeof(payload)='object'
        AND payload->>'id'=id AND payload->>'family'=family
        AND (payload->>'started_at')::timestamptz=started_at)
);
CREATE INDEX IF NOT EXISTS mantik_strategy_trials_family ON mantik_strategy_trials(family);
CREATE TABLE IF NOT EXISTS mantik_strategy_trial_returns (
    trial_id text NOT NULL REFERENCES mantik_strategy_trials(id),
    session_date date NOT NULL CHECK (session_date >= DATE '1900-01-01'),
    daily_return text NOT NULL CHECK (daily_return ~ '^-?(0|[1-9][0-9]{0,29})(\.[0-9]{1,18})?$'),
    equity text NOT NULL CHECK (equity ~ '^-?(0|[1-9][0-9]{0,29})(\.[0-9]{1,18})?$'),
    PRIMARY KEY (trial_id,session_date)
);
CREATE TABLE IF NOT EXISTS mantik_strategy_trial_outcomes (
    trial_id text PRIMARY KEY REFERENCES mantik_strategy_trials(id),
    status text NOT NULL CHECK (status IN ('completed','failed','crashed','abandoned')),
    finished_at timestamptz NOT NULL
);
CREATE OR REPLACE FUNCTION mantik_strategy_trial_append() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE
    started timestamptz;
BEGIN
    IF TG_OP<>'INSERT' THEN
        RAISE EXCEPTION 'strategy trial history is append-only' USING ERRCODE='23514';
    END IF;
    IF TG_TABLE_NAME='mantik_strategy_trials' THEN
        IF EXISTS(SELECT 1 FROM json_array_elements_text(NEW.payload->'parents') parent(id)
                  LEFT JOIN mantik_strategy_trials t ON t.id=parent.id
                  WHERE t.id IS NULL OR t.started_at>NEW.started_at) THEN
            RAISE EXCEPTION 'strategy trial parent is missing or later' USING ERRCODE='23514';
        END IF;
    ELSE
        -- The parent lock serializes return appends with completion, including
        -- direct SQL writes; no daily return can race past a terminal outcome.
        SELECT started_at INTO started FROM mantik_strategy_trials WHERE id=NEW.trial_id FOR UPDATE;
        IF TG_TABLE_NAME='mantik_strategy_trial_returns' THEN
            IF EXISTS(SELECT 1 FROM mantik_strategy_trial_outcomes WHERE trial_id=NEW.trial_id)
               OR EXISTS(SELECT 1 FROM mantik_strategy_trial_returns WHERE trial_id=NEW.trial_id AND session_date>=NEW.session_date) THEN
                RAISE EXCEPTION 'strategy trial return is closed or out of order' USING ERRCODE='23514';
            END IF;
        ELSIF NEW.finished_at<started THEN
            RAISE EXCEPTION 'strategy trial outcome predates its start' USING ERRCODE='23514';
        END IF;
    END IF;
    RETURN NEW;
END;
$$;
DROP TRIGGER IF EXISTS mantik_strategy_trial_append ON mantik_strategy_trials;
CREATE TRIGGER mantik_strategy_trial_append BEFORE INSERT OR UPDATE OR DELETE ON mantik_strategy_trials
FOR EACH ROW EXECUTE FUNCTION mantik_strategy_trial_append();
DROP TRIGGER IF EXISTS mantik_strategy_trial_return_append ON mantik_strategy_trial_returns;
CREATE TRIGGER mantik_strategy_trial_return_append BEFORE INSERT OR UPDATE OR DELETE ON mantik_strategy_trial_returns
FOR EACH ROW EXECUTE FUNCTION mantik_strategy_trial_append();
DROP TRIGGER IF EXISTS mantik_strategy_trial_outcome_append ON mantik_strategy_trial_outcomes;
CREATE TRIGGER mantik_strategy_trial_outcome_append BEFORE INSERT OR UPDATE OR DELETE ON mantik_strategy_trial_outcomes
FOR EACH ROW EXECUTE FUNCTION mantik_strategy_trial_append();
DROP TRIGGER IF EXISTS mantik_strategy_trial_no_truncate ON mantik_strategy_trials;
CREATE TRIGGER mantik_strategy_trial_no_truncate BEFORE TRUNCATE ON mantik_strategy_trials
FOR EACH STATEMENT EXECUTE FUNCTION mantik_strategy_trial_append();
DROP TRIGGER IF EXISTS mantik_strategy_trial_return_no_truncate ON mantik_strategy_trial_returns;
CREATE TRIGGER mantik_strategy_trial_return_no_truncate BEFORE TRUNCATE ON mantik_strategy_trial_returns
FOR EACH STATEMENT EXECUTE FUNCTION mantik_strategy_trial_append();
DROP TRIGGER IF EXISTS mantik_strategy_trial_outcome_no_truncate ON mantik_strategy_trial_outcomes;
CREATE TRIGGER mantik_strategy_trial_outcome_no_truncate BEFORE TRUNCATE ON mantik_strategy_trial_outcomes
FOR EACH STATEMENT EXECUTE FUNCTION mantik_strategy_trial_append();
