-- Admission and its provenance-bearing news record commit together. The attempt
-- remains immutable; one reviewed admission belongs to one retained attempt.
CREATE TABLE mantik_news_extraction_admissions (
 id TEXT PRIMARY KEY REFERENCES mantik_news_extractions(id),
 payload JSONB NOT NULL,
 CONSTRAINT mantik_extraction_admission_shape CHECK (COALESCE(
  jsonb_typeof(payload)='object'
  AND payload->>'schema'='mantik-reviewed-extraction-admission-v1'
  AND payload->'command'->>'attempt_id'=id
  AND payload->'command'->>'receiver_id' ~ '^receiver-[a-zA-Z0-9_-]{16,64}$'
  AND payload->'command'->>'approval_code'='approve_claim_candidates_v1'
  AND payload->'command'->>'attempt_sha256' ~ '^[a-f0-9]{64}$'
  AND payload->'command'->>'result_sha256' ~ '^[a-f0-9]{64}$'
  AND payload->'news_receipt'->>'record_key' ~ '^[a-f0-9]{64}$'
  AND payload->>'authority'='reviewed_recorded_claims_only'
  AND payload->'qualification_credit'='false'::jsonb
  AND payload->>'sha256' ~ '^[a-f0-9]{64}$'
 ,false))
);
CREATE UNIQUE INDEX mantik_extraction_admission_record ON mantik_news_extraction_admissions ((payload->'news_receipt'->>'record_key'));
