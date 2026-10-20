-- Old/manual registrations retain NULL; legacy JSON and hashes do not change.
ALTER TABLE mantik_candidate_registrations
 ADD COLUMN generation_link_sha256 TEXT CHECK(generation_link_sha256 ~ '^[a-f0-9]{64}$'),
 ADD CONSTRAINT mantik_candidate_link_identity UNIQUE(id,generation_link_sha256);
CREATE TABLE mantik_generated_candidate_links (
 id TEXT PRIMARY KEY,
 sha256 TEXT NOT NULL CHECK(sha256 ~ '^[a-f0-9]{64}$'),
 payload JSONB NOT NULL CHECK(octet_length(payload::text)<=4096),
 UNIQUE(id,sha256),
 FOREIGN KEY(id,sha256) REFERENCES mantik_candidate_registrations(id,generation_link_sha256)
 DEFERRABLE INITIALLY DEFERRED,
 CHECK(COALESCE(jsonb_typeof(payload)='object'
 AND payload->>'schema'='mantik-generated-candidate-registration-link-v1'
 AND payload->'command'->>'request_id'=id AND payload->>'sha256'=sha256
 AND payload->>'authority'='recorded_generation_registration_link_only'
 AND payload->>'provider_called'='false'
 AND payload->>'model_authorship_verified'='false'
 AND payload->>'source_compiled'='false' AND payload->>'source_executed'='false'
 AND payload->>'qualification_credit'='false',false))
);
ALTER TABLE mantik_candidate_registrations
 ADD CONSTRAINT mantik_candidate_link_retention FOREIGN KEY(id,generation_link_sha256)
 REFERENCES mantik_generated_candidate_links(id,sha256) DEFERRABLE INITIALLY DEFERRED;
