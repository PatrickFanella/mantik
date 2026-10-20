-- Immutable pre-execution intent, not a host execution or outcome receipt.
CREATE TABLE mantik_candidate_worker_intents (
 id TEXT PRIMARY KEY CHECK (id ~ '^[a-zA-Z0-9_-]{1,128}$'),
 sequence INTEGER UNIQUE NOT NULL CHECK (sequence BETWEEN 1 AND 64),
 registration_id TEXT NOT NULL REFERENCES mantik_candidate_registrations(id),
 payload JSONB NOT NULL,
 CHECK (COALESCE(jsonb_typeof(payload)='object'
 AND payload->>'schema'='mantik-candidate-worker-intent-v1'
 AND payload->'command'->>'request_id'=id
 AND payload->'command'->>'registration_id'=registration_id
 AND (payload->>'sequence')::integer=sequence
 AND payload->>'status'='intent_retained'
 AND payload->>'authority'='candidate_worker_intent_only'
 AND payload->>'execution_started'='false'
 AND payload->>'host_state_known'='false'
 AND payload->>'qualification_credit'='false'
 AND payload->>'sha256' ~ '^[a-f0-9]{64}$',false)),
 CHECK (octet_length(payload::text)<=1048576)
);
