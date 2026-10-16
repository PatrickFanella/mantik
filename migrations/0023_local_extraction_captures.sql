-- Imports are immutable caller reports, separate from recorded fixture intents.
CREATE TABLE mantik_local_extraction_captures (
 id TEXT PRIMARY KEY,
 payload JSONB NOT NULL,
 CONSTRAINT mantik_local_capture_shape CHECK (COALESCE(
  jsonb_typeof(payload)='object'
  AND payload->>'schema'='mantik-local-extraction-capture-v1'
  AND payload->>'id'=id
  AND id ~ '^[a-zA-Z0-9_-]{1,80}$'
  AND payload->>'receiver_id' ~ '^receiver-[a-zA-Z0-9_-]{16,64}$'
  AND payload->>'mode'='caller_reported_local_capture'
  AND payload->>'authority'='caller_reported_local_capture_only'
  AND payload->'execution_attested'='false'::jsonb
  AND payload->'qualification_credit'='false'::jsonb
  AND payload->>'sha256' ~ '^[a-f0-9]{64}$'
  AND octet_length(payload::text)<=33685504
 ,false))
);
