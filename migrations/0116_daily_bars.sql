-- Raw (unadjusted) daily bars, one row per asset, session date and revision.
-- Tickers are reused, so the key is the source's asset identity; the symbol is
-- only the ticker a bar was reported under. Prices are exact decimals.
--
-- published_at is when the source published the bar, observed_at is when
-- Mantik fetched it, and available_at is the earliest clock at which a reader
-- may use it. Rows never change. A changed bar is the next revision, and it
-- must become available after the revision it replaces, so a read as of an
-- earlier clock keeps its answer.
CREATE TABLE IF NOT EXISTS mantik_daily_bars (
    asset_id text NOT NULL CHECK (asset_id ~ '^[A-Za-z0-9][A-Za-z0-9:._-]{0,63}$'),
    bar_date date NOT NULL CHECK (bar_date >= DATE '1900-01-01'),
    revision integer NOT NULL CHECK (revision >= 1),
    symbol text NOT NULL CHECK (symbol ~ '^[A-Z][A-Z0-9.]{0,15}$'),
    open_price numeric NOT NULL CHECK (open_price > 0),
    high_price numeric NOT NULL CHECK (high_price > 0),
    low_price numeric NOT NULL CHECK (low_price > 0),
    close_price numeric NOT NULL CHECK (close_price > 0),
    vwap numeric NOT NULL CHECK (vwap >= 0),
    volume bigint NOT NULL CHECK (volume >= 0),
    trade_count bigint NOT NULL CHECK (trade_count >= 0),
    published_at timestamptz NOT NULL,
    observed_at timestamptz NOT NULL,
    available_at timestamptz NOT NULL CHECK (available_at >= published_at),
    PRIMARY KEY (asset_id, bar_date, revision)
);
CREATE OR REPLACE FUNCTION mantik_daily_bar() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF TG_OP<>'INSERT' THEN
        RAISE EXCEPTION 'daily bar is immutable' USING ERRCODE='23514';
    END IF;
    IF NEW.revision>1 AND NOT EXISTS(SELECT 1 FROM mantik_daily_bars
                  WHERE asset_id=NEW.asset_id AND bar_date=NEW.bar_date AND revision=NEW.revision-1
                    AND available_at<NEW.available_at AND observed_at<=NEW.observed_at) THEN
        RAISE EXCEPTION 'daily bar correction must follow its predecessor' USING ERRCODE='23514';
    END IF;
    RETURN NEW;
END;
$$;
DROP TRIGGER IF EXISTS mantik_daily_bar ON mantik_daily_bars;
CREATE TRIGGER mantik_daily_bar BEFORE INSERT OR UPDATE OR DELETE ON mantik_daily_bars
FOR EACH ROW EXECUTE FUNCTION mantik_daily_bar();
