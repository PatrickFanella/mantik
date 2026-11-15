CREATE TABLE IF NOT EXISTS mantik_kalshi_weather_predictions (
 id text PRIMARY KEY,
 market_ticker text NOT NULL,
 model_version text NOT NULL,
 as_of timestamptz NOT NULL,
 close_time timestamptz NOT NULL,
 recorded_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 probability double precision NOT NULL CHECK (probability >= 0 AND probability <= 1),
 payload bytea NOT NULL,
 CHECK (as_of <= recorded_at AND recorded_at < close_time)
);
CREATE INDEX IF NOT EXISTS mantik_kalshi_weather_predictions_market ON mantik_kalshi_weather_predictions(market_ticker, recorded_at);
CREATE TABLE IF NOT EXISTS mantik_kalshi_weather_settlements (
 market_ticker text PRIMARY KEY,
 observed_at timestamptz NOT NULL,
 recorded_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 payload jsonb NOT NULL
);
CREATE OR REPLACE FUNCTION mantik_kalshi_weather_admit() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 NEW.recorded_at := clock_timestamp();
 IF NEW.as_of > NEW.recorded_at OR NEW.recorded_at >= NEW.close_time THEN
  RAISE EXCEPTION 'Weather predictions require available inputs and pre-close storage';
 END IF;
 RETURN NEW;
END; $$;
DROP TRIGGER IF EXISTS mantik_kalshi_weather_admit ON mantik_kalshi_weather_predictions;
CREATE TRIGGER mantik_kalshi_weather_admit BEFORE INSERT ON mantik_kalshi_weather_predictions FOR EACH ROW EXECUTE FUNCTION mantik_kalshi_weather_admit();
DROP TRIGGER IF EXISTS mantik_kalshi_weather_predictions_append_only ON mantik_kalshi_weather_predictions;
CREATE TRIGGER mantik_kalshi_weather_predictions_append_only BEFORE UPDATE OR DELETE ON mantik_kalshi_weather_predictions FOR EACH ROW EXECUTE FUNCTION mantik_kalshi_append_only();
DROP TRIGGER IF EXISTS mantik_kalshi_weather_predictions_no_truncate ON mantik_kalshi_weather_predictions;
CREATE TRIGGER mantik_kalshi_weather_predictions_no_truncate BEFORE TRUNCATE ON mantik_kalshi_weather_predictions FOR EACH STATEMENT EXECUTE FUNCTION mantik_kalshi_no_truncate();
DROP TRIGGER IF EXISTS mantik_kalshi_weather_settlements_append_only ON mantik_kalshi_weather_settlements;
CREATE TRIGGER mantik_kalshi_weather_settlements_append_only BEFORE UPDATE OR DELETE ON mantik_kalshi_weather_settlements FOR EACH ROW EXECUTE FUNCTION mantik_kalshi_append_only();
DROP TRIGGER IF EXISTS mantik_kalshi_weather_settlements_no_truncate ON mantik_kalshi_weather_settlements;
CREATE TRIGGER mantik_kalshi_weather_settlements_no_truncate BEFORE TRUNCATE ON mantik_kalshi_weather_settlements FOR EACH STATEMENT EXECUTE FUNCTION mantik_kalshi_no_truncate();
