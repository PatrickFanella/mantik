CREATE TABLE mantik_decision_reviews (
    id text PRIMARY KEY,
    payload bytea NOT NULL CHECK (octet_length(payload) <= 32768)
);
