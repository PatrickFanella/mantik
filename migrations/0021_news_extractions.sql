-- Extraction attempts retain source pins before completion. They do not enter
-- news admission or carry publication/execution authority. Older binaries must
-- refuse schema 21 rather than overlook this ledger during recovery.
CREATE TABLE mantik_news_extractions (
 id TEXT PRIMARY KEY CHECK (id ~ '^[a-zA-Z0-9_-]{1,80}$'),
 payload JSONB NOT NULL,
 CONSTRAINT mantik_extraction_shape CHECK (COALESCE(
  jsonb_typeof(payload)='object'
  AND payload->>'schema'='mantik-news-extraction-attempt-v1'
  AND payload->>'id'=id
  AND payload->>'receiver_id' ~ '^receiver-[a-zA-Z0-9_-]{16,64}$'
  AND payload->>'mode'='recorded_model_fixture'
  AND payload->>'status' IN ('pending','completed','expired')
  AND jsonb_typeof(payload->'article')='object'
  AND payload->'article'->>'schema'='mantik-news-article-v1'
  AND jsonb_typeof(payload->'contract')='object'
  AND payload->'contract'->>'schema'='mantik-news-extraction-contract-v1'
  AND payload->>'article_sha256' ~ '^[a-f0-9]{64}$'
  AND payload->>'contract_sha256' ~ '^[a-f0-9]{64}$'
  AND payload->>'sha256' ~ '^[a-f0-9]{64}$'
 ,false))
);
