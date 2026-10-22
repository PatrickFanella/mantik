CREATE TABLE mantik_paper_outcomes (
 id text PRIMARY KEY,
 payload bytea NOT NULL CHECK (octet_length(payload) <= 4194304)
);
