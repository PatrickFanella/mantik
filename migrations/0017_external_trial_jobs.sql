CREATE TABLE mantik_external_trial_jobs (
 id text PRIMARY KEY,
 attempt_id text NOT NULL REFERENCES mantik_trial_runs(attempt_id),
 payload jsonb NOT NULL CHECK (
  payload ?& ARRAY['schema','command','trial_id','hypothesis_id','registration_sha256','declared_at','status','execution_started','host_state_known','job','authority','qualification_credit','sha256']
  AND payload->>'schema'='mantik-external-trial-job-v1'
  AND payload->'command'->>'request_id'=id
  AND payload->'command'->>'attempt_id'=attempt_id
  AND payload->>'status'='declared'
  AND payload->>'execution_started'='false'
  AND payload->>'host_state_known'='false'
  AND payload->>'qualification_credit'='false'
  AND payload->>'authority'='research_external_job_declaration_only'
 )
);
CREATE TABLE mantik_external_trial_job_inputs (
 id text NOT NULL REFERENCES mantik_external_trial_jobs(id),
 reference text NOT NULL CHECK(reference IN ('dataset.json','recipe.json','native.json')),
 content text NOT NULL CHECK(octet_length(content) BETWEEN 1 AND 8388608),
 PRIMARY KEY(id,reference)
);
