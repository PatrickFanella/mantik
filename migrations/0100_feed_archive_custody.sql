-- Retain complete source snapshots before archive-aware readers remove any
-- active copies. The source hash makes a repeat of the same cut idempotent.
CREATE TABLE mantik_feed_archives (
    sequence bigint PRIMARY KEY CHECK (sequence > 0),
    source_sha256 text NOT NULL UNIQUE CHECK (source_sha256 ~ '^[a-f0-9]{64}$'),
    sha256 text NOT NULL UNIQUE CHECK (sha256 ~ '^[a-f0-9]{64}$'),
    header bytea NOT NULL CHECK (octet_length(header) <= 4096),
    payload bytea NOT NULL CHECK (octet_length(payload) <= 67108864)
);
COMMENT ON TABLE mantik_feed_archives IS 'Immutable receiver-owned feed/collector/routing source snapshots chained in sequence; active copies remain until archive-aware replay is delivered';
