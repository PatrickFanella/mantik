CREATE TABLE mantik_owned_admission_reviews (
    id text PRIMARY KEY,
    payload bytea NOT NULL CHECK (octet_length(payload) <= 32768)
);
COMMENT ON TABLE mantik_owned_admission_reviews IS 'Service-owned non-grant admission review chains; no admission or financial authority';
