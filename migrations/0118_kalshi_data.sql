CREATE TABLE IF NOT EXISTS mantik_kalshi_markets (
  ticker text NOT NULL, observed_at timestamptz NOT NULL, payload jsonb NOT NULL, original text NOT NULL, sha256 text NOT NULL,
  PRIMARY KEY(ticker, observed_at)
);
CREATE INDEX IF NOT EXISTS mantik_kalshi_markets_asof ON mantik_kalshi_markets(ticker, observed_at);
CREATE TABLE IF NOT EXISTS mantik_kalshi_order_books (
  market_ticker text NOT NULL, observed_at timestamptz NOT NULL, payload jsonb NOT NULL, original text NOT NULL, sha256 text NOT NULL,
  PRIMARY KEY(market_ticker, observed_at)
);
CREATE INDEX IF NOT EXISTS mantik_kalshi_order_books_asof ON mantik_kalshi_order_books(market_ticker, observed_at);
CREATE OR REPLACE FUNCTION mantik_kalshi_append_only() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN RAISE EXCEPTION 'Kalshi source records are append-only'; END; $$;
DROP TRIGGER IF EXISTS mantik_kalshi_order_books_append_only ON mantik_kalshi_order_books;
CREATE TRIGGER mantik_kalshi_order_books_append_only BEFORE UPDATE OR DELETE ON mantik_kalshi_order_books FOR EACH ROW EXECUTE FUNCTION mantik_kalshi_append_only();
DROP TRIGGER IF EXISTS mantik_kalshi_markets_append_only ON mantik_kalshi_markets;
CREATE TRIGGER mantik_kalshi_markets_append_only BEFORE UPDATE OR DELETE ON mantik_kalshi_markets FOR EACH ROW EXECUTE FUNCTION mantik_kalshi_append_only();
CREATE OR REPLACE FUNCTION mantik_kalshi_no_truncate() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN RAISE EXCEPTION 'Kalshi source records cannot be truncated'; END; $$;
DROP TRIGGER IF EXISTS mantik_kalshi_markets_no_truncate ON mantik_kalshi_markets;
CREATE TRIGGER mantik_kalshi_markets_no_truncate BEFORE TRUNCATE ON mantik_kalshi_markets EXECUTE FUNCTION mantik_kalshi_no_truncate();
DROP TRIGGER IF EXISTS mantik_kalshi_order_books_no_truncate ON mantik_kalshi_order_books;
CREATE TRIGGER mantik_kalshi_order_books_no_truncate BEFORE TRUNCATE ON mantik_kalshi_order_books EXECUTE FUNCTION mantik_kalshi_no_truncate();
