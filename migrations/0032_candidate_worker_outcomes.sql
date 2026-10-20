-- Immutable caller-reported captures. No execution or qualification authority.
CREATE TABLE mantik_candidate_worker_outcomes (
 id TEXT PRIMARY KEY REFERENCES mantik_candidate_worker_intents(id),
 sequence INTEGER UNIQUE NOT NULL CHECK (sequence BETWEEN 1 AND 64),
 payload JSONB NOT NULL,
 capture BYTEA NOT NULL CHECK (octet_length(capture) BETWEEN 1 AND 25165824),
 CHECK (COALESCE(jsonb_typeof(payload)='object'
 AND payload->>'schema'='mantik-candidate-worker-outcome-v1'
 AND payload->>'request_id'=id
 AND (payload->>'intent_sequence')::integer=sequence
 AND payload->>'authority'='retained_caller_reported_candidate_outcome_only'
 AND payload->>'execution_verified'='false'
 AND payload->>'current_host_state_known'='false'
 AND payload->>'qualification_credit'='false'
 AND payload->>'capture_sha256' ~ '^[a-f0-9]{64}$'
 AND payload->>'sha256' ~ '^[a-f0-9]{64}$',false)),
 CHECK (octet_length(payload::text)<=8192)
);
