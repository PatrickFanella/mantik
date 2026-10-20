-- One atomic row retains both derived metadata and the complete caller recording.
CREATE TABLE mantik_candidate_generation_outcomes (
 id TEXT PRIMARY KEY REFERENCES mantik_candidate_generation_intents(id),
 sequence INTEGER UNIQUE NOT NULL CHECK(sequence BETWEEN 1 AND 64),
 payload JSONB NOT NULL,
 recording BYTEA NOT NULL CHECK(octet_length(recording) BETWEEN 1 AND 4194304),
 CHECK(octet_length(payload::text)<=8192),
 CHECK(COALESCE(jsonb_typeof(payload)='object'
 AND payload->>'schema'='mantik-candidate-generation-outcome-v1'
 AND payload->>'request_id'=id
 AND (payload->>'intent_sequence')::integer=sequence
 AND payload->>'status' IN ('candidate_recorded','output_rejected')
 AND payload->>'authority'='retained_caller_reported_candidate_generation_only'
 AND payload->>'provider_called'='false'
 AND payload->>'model_identity_verified'='false'
 AND payload->>'model_authorship_verified'='false'
 AND payload->>'source_compiled'='false'
 AND payload->>'source_executed'='false'
 AND payload->>'qualification_credit'='false'
 AND payload->>'sha256' ~ '^[a-f0-9]{64}$',false))
);
