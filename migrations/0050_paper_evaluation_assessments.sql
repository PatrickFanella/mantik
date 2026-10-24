CREATE TABLE mantik_paper_evaluation_assessments (
    id text PRIMARY KEY,
    payload bytea NOT NULL CHECK (octet_length(payload) <= 8454144)
);
COMMENT ON TABLE mantik_paper_evaluation_assessments IS 'Service-issued incomplete evaluation assessments; no admission, execution or qualification authority';
