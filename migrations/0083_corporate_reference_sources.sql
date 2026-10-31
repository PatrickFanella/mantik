CREATE TABLE IF NOT EXISTS mantik_corporate_reference_sources (
 id text PRIMARY KEY,
 sequence integer NOT NULL UNIQUE CHECK(sequence BETWEEN 1 AND 32),
 acquisition_sha256 text NOT NULL UNIQUE,
 payload jsonb NOT NULL,
 originals bytea[] NOT NULL
);
COMMENT ON TABLE mantik_corporate_reference_sources IS 'schema83: service custody of configured-journal Polygon and issuer-document originals; no provider, coverage, economic or admission authority';
