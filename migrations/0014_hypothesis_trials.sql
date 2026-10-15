-- Immutable pre-execution plans; no dataset/results/worker authority is admitted.
CREATE TABLE mantik_hypothesis_trials (
 id text PRIMARY KEY CHECK (id ~ '^[a-zA-Z0-9_-]{1,128}$'),
 hypothesis_id text NOT NULL CHECK (hypothesis_id ~ '^hypothesis-[a-f0-9]{64}$'),
 publication_id text NOT NULL REFERENCES mantik_hypothesis_publications(id),
 parent_id text REFERENCES mantik_hypothesis_trials(id),
 payload jsonb NOT NULL CHECK (
  payload ?& ARRAY['schema','id','receiver_id','hypothesis_id','publication_id','publication_sha256','declaration_sha256','registered_at','status','authority','qualification_credit','dataset_availability_verified','recipe','sha256']
  AND payload->>'schema'='mantik-hypothesis-trial-registration-v1'
  AND payload->>'id'=id AND payload->>'hypothesis_id'=hypothesis_id
  AND payload->>'publication_id'=publication_id
  AND coalesce(payload->>'parent_id','')=coalesce(parent_id,'')
  AND payload->>'status'='registered'
  AND payload->>'authority'='research_trial_declaration_only'
  AND payload->>'qualification_credit'='false'
  AND payload->>'dataset_availability_verified'='false'
 )
);
CREATE INDEX mantik_trials_hypothesis ON mantik_hypothesis_trials(hypothesis_id);
