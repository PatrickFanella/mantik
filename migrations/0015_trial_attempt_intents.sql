-- Pre-execution intents only. A future executor needs its own reviewed schema.
CREATE TABLE mantik_trial_attempts (
 id text PRIMARY KEY CHECK(id ~ '^[a-zA-Z0-9_-]{1,128}$'),
 trial_id text NOT NULL REFERENCES mantik_hypothesis_trials(id),
 payload jsonb NOT NULL CHECK(
  payload ?& ARRAY['schema','id','trial_id','receiver_id','registration_sha256','recipe_sha256','dataset_sha256','started_at','intent_sha256','status','execution_started','qualification_credit','authority','sha256']
  AND payload->>'schema'='mantik-trial-attempt-intent-v1'
  AND payload->>'id'=id AND payload->>'trial_id'=trial_id
  AND payload->>'status' IN ('pending','cancelled')
  AND payload->>'execution_started'='false'
  AND payload->>'qualification_credit'='false'
  AND payload->>'authority'='research_attempt_intent_only'
 )
);
CREATE INDEX mantik_attempts_trial ON mantik_trial_attempts(trial_id);
