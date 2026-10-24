-- Fence older writers that can backdate collector/source writes into retained observations.
-- Preserve all existing records and historical bytes.
COMMENT ON TABLE mantik_feed_collectors IS 'Collector ownership and source clocks; new writes are strictly later than retained decision, outcome and assessment cuts';
