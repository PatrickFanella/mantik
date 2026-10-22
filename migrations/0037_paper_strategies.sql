CREATE TABLE mantik_paper_strategies (
    id text PRIMARY KEY,
    payload jsonb NOT NULL CHECK (jsonb_typeof(payload) = 'object')
);
