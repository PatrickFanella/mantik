CREATE TABLE IF NOT EXISTS mantik_corporate_action_sources (
 id text PRIMARY KEY,
 sequence integer NOT NULL UNIQUE CHECK(sequence BETWEEN 1 AND 32),
 payload jsonb NOT NULL
);
COMMENT ON TABLE mantik_corporate_action_sources IS 'schema71: service custody of caller-reported original corporate-action pages; no provider, coverage or economic authority';
