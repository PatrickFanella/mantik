-- Separate synthetic assertions from immutable account funding and execution.
CREATE TABLE mantik_paper_ledger_rehearsals (
 account_id TEXT PRIMARY KEY REFERENCES mantik_paper_accounts(id),
 payload JSONB NOT NULL,
 CONSTRAINT mantik_paper_ledger_rehearsal_shape CHECK (COALESCE(
 jsonb_typeof(payload)='object'
 AND payload->>'schema'='mantik-paper-ledger-rehearsal-v1'
 AND payload->'account'->'config'->>'id'=account_id
 AND jsonb_typeof(payload->'accepted')='array'
 AND jsonb_array_length(payload->'accepted') BETWEEN 1 AND 256
 AND payload->>'authority'='service_retained_synthetic_ledger_rehearsal_only'
 AND payload->'allocation_available'='false'::jsonb
 AND payload->'execution_attested'='false'::jsonb
 AND payload->'qualification_credit'='false'::jsonb
 AND payload->>'sha256' ~ '^[a-f0-9]{64}$'
 ,false))
);
