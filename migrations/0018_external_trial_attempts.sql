CREATE TABLE mantik_external_trial_attempts (
 id text PRIMARY KEY,
 job_id text NOT NULL REFERENCES mantik_external_trial_jobs(id),
 payload jsonb NOT NULL,
 CHECK (payload->'intent'->>'schema'='mantik-external-trial-attempt-v1'),
 CHECK (payload->'intent'->'command'->>'request_id'=id),
 CHECK (payload->'intent'->'command'->>'job_id'=job_id),
 CHECK (payload->'intent'->>'authority'='research_external_attempt_intent_only'),
 CHECK (payload->'intent'->>'execution_started'='false'),
 CHECK (payload->'intent'->>'host_state_known'='false'),
 CHECK (payload->'intent'->>'qualification_credit'='false'),
 CHECK (payload->'outcome'='null'::jsonb OR (
   payload->'outcome'->>'schema'='mantik-external-trial-outcome-v1' AND
   payload->'outcome'->>'attempt_id'=id AND
   payload->'outcome'->>'authority'='caller_reported_external_attempt_only' AND
   payload->'outcome'->>'host_state_known'='false' AND
   payload->'outcome'->>'qualification_credit'='false'
 ))
);
CREATE TABLE mantik_external_trial_attempt_outputs (
 id text PRIMARY KEY REFERENCES mantik_external_trial_attempts(id),
 stdout text NOT NULL CHECK (octet_length(stdout)<=16777216),
 stderr text NOT NULL CHECK (octet_length(stderr)<=65536)
);
