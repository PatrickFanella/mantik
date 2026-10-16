-- Local approval and its provenance-bearing news record commit together. The
-- caller-reported capture remains immutable and grants no execution attestation.
CREATE TABLE mantik_local_capture_admissions (
 id TEXT PRIMARY KEY REFERENCES mantik_local_extraction_captures(id),
 payload JSONB NOT NULL,
 CONSTRAINT mantik_local_capture_admission_shape CHECK (COALESCE(
  jsonb_typeof(payload)='object'
  AND payload->>'schema'='mantik-reviewed-local-capture-admission-v1'
  AND payload->'command'->>'capture_id'=id
  AND payload->'command'->>'receiver_id' ~ '^receiver-[a-zA-Z0-9_-]{16,64}$'
  AND payload->'command'->>'approval_code'='approve_local_claim_candidates_v1'
  AND payload->'command'->>'capture_sha256' ~ '^[a-f0-9]{64}$'
  AND payload->'command'->>'result_sha256' ~ '^[a-f0-9]{64}$'
  AND payload->'news_receipt'->>'record_key' ~ '^[a-f0-9]{64}$'
  AND payload->>'authority'='reviewed_caller_reported_local_claims_only'
  AND payload->'execution_attested'='false'::jsonb
  AND payload->'qualification_credit'='false'::jsonb
  AND payload->>'sha256' ~ '^[a-f0-9]{64}$'
 ,false))
);
CREATE UNIQUE INDEX mantik_local_capture_admission_record ON mantik_local_capture_admissions ((payload->'news_receipt'->>'record_key'));
