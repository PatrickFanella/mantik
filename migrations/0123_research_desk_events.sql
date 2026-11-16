-- Immutable research-desk sessions; installed by the storage bootstrap.
CREATE TABLE IF NOT EXISTS mantik_research_desk_events (
 session_id text NOT NULL CHECK (session_id ~ '^[a-zA-Z0-9][a-zA-Z0-9._-]{0,79}$'),
 sequence integer NOT NULL CHECK (sequence BETWEEN 0 AND 49),
 kind text NOT NULL CHECK (kind IN ('start','call','reply','result','done')),
 at timestamptz NOT NULL,
 body jsonb NOT NULL CHECK (octet_length(body::text) <= 2097152),
 PRIMARY KEY(session_id,sequence)
);
CREATE OR REPLACE FUNCTION mantik_guard_research_desk_events() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 RAISE EXCEPTION 'research desk history is append-only';
END;
$$;
DROP TRIGGER IF EXISTS mantik_research_desk_immutable ON mantik_research_desk_events;
CREATE TRIGGER mantik_research_desk_immutable BEFORE UPDATE OR DELETE OR TRUNCATE ON mantik_research_desk_events
 FOR EACH STATEMENT EXECUTE FUNCTION mantik_guard_research_desk_events();
