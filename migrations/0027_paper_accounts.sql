-- Immutable operator-declared paper capital. No broker state or execution link.
CREATE TABLE mantik_paper_accounts (
 id TEXT PRIMARY KEY,
 payload JSONB NOT NULL,
 CONSTRAINT mantik_paper_account_shape CHECK (COALESCE(
 jsonb_typeof(payload)='object'
 AND id ~ '^[a-zA-Z0-9_-]{1,128}$'
 AND payload->>'schema'='mantik-paper-account-v1'
 AND payload->>'receiver_id' ~ '^receiver-[a-zA-Z0-9_-]{16,64}$'
 AND payload->'config'->>'id'=id
 AND payload->'config'->>'currency'='USD'
 AND jsonb_typeof(payload->'config'->'opening_cash_cents')='number'
 AND (payload->'config'->>'opening_cash_cents')::numeric BETWEEN 1 AND 1000000000000
 AND jsonb_typeof(payload->'config'->'capital_limit_cents')='number'
 AND (payload->'config'->>'capital_limit_cents')::numeric BETWEEN 1 AND (payload->'config'->>'opening_cash_cents')::numeric
 AND payload->>'authority'='operator_declared_paper_capital_only'
 AND payload->>'policy'='cash_only_no_leverage_v1'
 AND payload->'allocation_available'='false'::jsonb
 AND payload->'qualification_credit'='false'::jsonb
 AND payload->>'sha256' ~ '^[a-f0-9]{64}$'
 ,false))
);
