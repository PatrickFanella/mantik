-- Legacy attempts have no profile and continue to denote news-review-v1.
-- Version fencing prevents schema-18 binaries from ignoring modern profiles.
ALTER TABLE mantik_news_reviews ADD CONSTRAINT mantik_review_profile CHECK (
 NOT (payload ? 'profile') OR COALESCE((
  jsonb_typeof(payload->'profile')='object'
  AND payload->'profile' ?& ARRAY['schema','contract','methods','sha256']
  AND payload->'profile'->>'schema'='mantik-review-profile-pin-v1'
  AND jsonb_typeof(payload->'profile'->'contract')='object'
  AND payload->'profile'->'contract'->>'type_id' IN ('news-review-v1','news-method-review-v1')
  AND jsonb_typeof(payload->'profile'->'methods')='array'
  AND payload->'profile'->>'sha256' ~ '^[a-f0-9]{64}$'
 ),false)
);
