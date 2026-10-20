-- Generation declarations and exact input artifacts; never provider calls.
CREATE TABLE mantik_candidate_generation_intents (
 id TEXT PRIMARY KEY CHECK(id ~ '^[a-zA-Z0-9_-]{1,128}$'),
 sequence INTEGER UNIQUE NOT NULL CHECK(sequence BETWEEN 1 AND 64),
 publication_id TEXT NOT NULL REFERENCES mantik_hypothesis_publications(id),
 payload JSONB NOT NULL,
 CHECK(octet_length(payload::text)<=1048576),
 CHECK(COALESCE(jsonb_typeof(payload)='object'
 AND payload->>'schema'='mantik-candidate-generation-intent-v1'
 AND payload->'command'->>'request_id'=id
 AND payload->'command'->>'publication_id'=publication_id
 AND (payload->>'sequence')::integer=sequence
 AND payload->>'status'='intent_retained'
 AND payload->>'authority'='candidate_generation_intent_only'
 AND payload->>'provider_called'='false'
 AND payload->>'model_identity_verified'='false'
 AND payload->>'source_generated'='false'
 AND payload->>'qualification_credit'='false'
 AND payload->>'sha256' ~ '^[a-f0-9]{64}$',false))
);
CREATE TABLE mantik_candidate_generation_datasets (
 id TEXT PRIMARY KEY REFERENCES mantik_candidate_generation_intents(id),
 content BYTEA NOT NULL CHECK(octet_length(content) BETWEEN 1 AND 8388608)
);
