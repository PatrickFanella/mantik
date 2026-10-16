-- Retain server-captured catalogue projections; schema-19 binaries must refuse
-- to ignore these pins or reinterpret the new reuse profile as a legacy review.
ALTER TABLE mantik_news_reviews DROP CONSTRAINT mantik_review_profile;
ALTER TABLE mantik_news_reviews ADD CONSTRAINT mantik_review_profile CHECK (
 NOT (payload ? 'profile') OR COALESCE((
  jsonb_typeof(payload->'profile')='object'
  AND payload->'profile' ?& ARRAY['schema','contract','methods','sha256']
  AND payload->'profile'->>'schema'='mantik-review-profile-pin-v1'
  AND jsonb_typeof(payload->'profile'->'contract')='object'
  AND payload->'profile'->'contract'->>'type_id' IN ('news-review-v1','news-method-review-v1','news-reuse-review-v1')
  AND jsonb_typeof(payload->'profile'->'methods')='array'
  AND payload->'profile'->>'sha256' ~ '^[a-f0-9]{64}$'
 ),false)
);
ALTER TABLE mantik_news_reviews ADD CONSTRAINT mantik_review_inventory CHECK (
 COALESCE((CASE WHEN payload->'profile'->'contract'->>'type_id'='news-reuse-review-v1' THEN
  payload ? 'hypothesis_inventory'
  AND jsonb_typeof(payload->'hypothesis_inventory')='object'
  AND payload->'hypothesis_inventory' ?& ARRAY['schema','receiver_id','catalogue_sha256','declarations','sha256']
  AND payload->'hypothesis_inventory'->>'schema'='mantik-review-hypothesis-inventory-v1'
  AND payload->'hypothesis_inventory'->>'receiver_id'=payload->'pin'->'subject'->>'receiver_id'
  AND payload->'hypothesis_inventory'->>'catalogue_sha256' ~ '^[a-f0-9]{64}$'
  AND payload->'hypothesis_inventory'->>'sha256' ~ '^[a-f0-9]{64}$'
  AND jsonb_typeof(payload->'hypothesis_inventory'->'declarations')='array'
  AND jsonb_array_length(payload->'hypothesis_inventory'->'declarations')<=32
 ELSE NOT (payload ? 'hypothesis_inventory') END),false)
);
