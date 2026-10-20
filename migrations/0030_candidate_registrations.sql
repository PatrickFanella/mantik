-- Candidate declarations and exact input bytes commit together. No worker or
-- execution authority is admitted; receiver locking serializes prefix appends.
CREATE TABLE mantik_candidate_registrations (
 id TEXT PRIMARY KEY CHECK (id ~ '^[a-zA-Z0-9_-]{1,128}$'),
 sequence INTEGER UNIQUE NOT NULL CHECK (sequence BETWEEN 1 AND 32),
 publication_id TEXT NOT NULL REFERENCES mantik_hypothesis_publications(id),
 parent_id TEXT REFERENCES mantik_candidate_registrations(id),
 payload JSONB NOT NULL,
 CHECK (COALESCE(jsonb_typeof(payload)='object'
 AND payload->>'schema'='mantik-candidate-registration-v1'
 AND payload->>'id'=id AND (payload->>'sequence')::integer=sequence
 AND payload->>'publication_id'=publication_id
 AND coalesce(payload->>'parent_id','')=coalesce(parent_id,'')
 AND payload->>'status'='registered'
 AND payload->>'authority'='candidate_registration_declaration_only'
 AND payload->>'qualification_credit'='false'
 AND payload->>'execution_started'='false'
 AND payload->>'dataset_availability_verified'='false'
 AND payload->>'sha256' ~ '^[a-f0-9]{64}$',false)),
 CHECK (octet_length(payload::text)<=1048576)
);
CREATE TABLE mantik_candidate_datasets (
 id TEXT PRIMARY KEY REFERENCES mantik_candidate_registrations(id),
 content BYTEA NOT NULL CHECK (octet_length(content) BETWEEN 1 AND 8388608)
);
