-- Retained option reference and indicative market observations, never OPRA quotes.
CREATE TABLE IF NOT EXISTS mantik_option_data (
    kind text NOT NULL CHECK (kind IN ('contract','bar','trade')),
    symbol text NOT NULL,
    source_key text NOT NULL,
    available_at timestamptz NOT NULL,
    observed_at timestamptz NOT NULL,
    feed text NOT NULL CHECK (feed IN ('reference','indicative')),
    record bytea NOT NULL,
    PRIMARY KEY(kind,symbol,source_key,available_at),
    CHECK (available_at >= observed_at),
    CHECK ((kind='contract' AND feed='reference') OR (kind IN ('bar','trade') AND feed='indicative'))
);
CREATE INDEX IF NOT EXISTS mantik_option_data_asof ON mantik_option_data(symbol,available_at);
CREATE OR REPLACE FUNCTION mantik_option_data_immutable() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN RAISE EXCEPTION 'option observations are append-only'; END $$;
DROP TRIGGER IF EXISTS mantik_option_data_immutable ON mantik_option_data;
CREATE TRIGGER mantik_option_data_immutable BEFORE UPDATE OR DELETE ON mantik_option_data
FOR EACH ROW EXECUTE FUNCTION mantik_option_data_immutable();

DROP TRIGGER IF EXISTS mantik_option_data_no_truncate ON mantik_option_data;
CREATE TRIGGER mantik_option_data_no_truncate BEFORE TRUNCATE ON mantik_option_data
FOR EACH STATEMENT EXECUTE FUNCTION mantik_option_data_immutable();
