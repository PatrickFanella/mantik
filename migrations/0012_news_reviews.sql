-- Durable recorded-model attempts; evidence is pinned by trusted application
-- storage and never supplied by the HTTP caller. No execution state changes.
CREATE TABLE mantik_news_reviews (
    id text PRIMARY KEY CHECK (id ~ '^[a-zA-Z0-9_-]{1,128}$'),
    payload jsonb NOT NULL CHECK (
        payload ?& ARRAY['schema','id','status','started_at','deadline','pin','model_mode']
        AND payload->>'schema' = 'mantik-news-review-attempt-v1'
        AND payload->>'model_mode' = 'recorded_model_fixture'
        AND payload->>'id' = id
        AND payload->>'status' IN ('pending','completed','superseded','expired')
    )
);
