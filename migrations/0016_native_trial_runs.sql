CREATE TABLE mantik_trial_runs (
 attempt_id text PRIMARY KEY REFERENCES mantik_trial_attempts(id),
 payload jsonb NOT NULL CHECK (
  payload ?& ARRAY['schema','attempt_id','invocation_id','intent_sha256','registration_sha256','recipe_sha256','input_sha256','started_at','deadline','status','qualification_credit','authority','sha256']
  AND payload->>'schema'='mantik-native-trial-run-v1'
  AND payload->>'attempt_id'=attempt_id
  AND payload->>'status' IN ('running','completed','failed','incomplete')
  AND payload->>'engine'='native-experiment-v1'
  AND payload->>'qualification_credit'='false'
  AND payload->>'authority'='synthetic_research_execution_only'
 )
);
CREATE TABLE mantik_trial_run_artifacts (
 attempt_id text PRIMARY KEY REFERENCES mantik_trial_runs(attempt_id),
 input_text text NOT NULL CHECK(octet_length(input_text) BETWEEN 1 AND 8388608),
 result_text text CHECK(octet_length(result_text) BETWEEN 1 AND 8388608)
);
