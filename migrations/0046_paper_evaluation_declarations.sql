CREATE TABLE mantik_paper_evaluation_declarations (
    id text PRIMARY KEY,
    payload bytea NOT NULL CHECK (octet_length(payload) <= 32768)
);
