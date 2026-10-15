-- Append-only research declarations. Reusing a question preserves every origin.
CREATE TABLE mantik_hypothesis_publications (
 id text PRIMARY KEY CHECK (id ~ '^[a-zA-Z0-9_-]{1,128}$'),
 question_id text NOT NULL CHECK (question_id ~ '^hypothesis-[a-f0-9]{64}$'),
 payload jsonb NOT NULL CHECK (
  payload ?& ARRAY['schema','id','receiver_id','review_id','review_result_id','subject','published_at','question_reused','origin_binding_verified','sealed']
  AND payload->>'schema'='mantik-hypothesis-publication-v1'
  AND payload->>'id'=id
  AND payload->'sealed'->>'id'=question_id
  AND payload->>'origin_binding_verified'='true'
  AND payload->'sealed'->>'authority'='research_declaration_only'
  AND payload->'sealed'->>'qualification_credit'='false'
 )
);
CREATE INDEX mantik_hypothesis_question ON mantik_hypothesis_publications(question_id);
