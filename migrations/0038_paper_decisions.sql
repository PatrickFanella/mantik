CREATE TABLE mantik_paper_decisions (
 id text PRIMARY KEY,
 payload bytea NOT NULL CHECK (octet_length(payload) <= 2097152)
);
