CREATE TABLE mantik_allocation_policies (
    id text PRIMARY KEY,
    account_id text NOT NULL UNIQUE REFERENCES mantik_paper_accounts(id),
    payload jsonb NOT NULL
);
