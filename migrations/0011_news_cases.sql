-- Research-only news evidence and cases use an independent atomic aggregate.
-- No existing fixture, routing, action or provider state is rewritten.
CREATE TABLE mantik_news_cases (
    singleton boolean PRIMARY KEY DEFAULT true CHECK (singleton),
    payload jsonb NOT NULL CHECK (
        payload ?& ARRAY['schema','policy','last_clock','records','cases']
        AND payload->>'schema' = 'mantik-news-cases-v1'
        AND payload->>'policy' = 'news-research-reopen-on-evidence-v1'
        AND jsonb_typeof(payload->'records') = 'object'
        AND jsonb_typeof(payload->'cases') = 'object'
    )
);
INSERT INTO mantik_news_cases(singleton,payload) VALUES
(true,'{"schema":"mantik-news-cases-v1","policy":"news-research-reopen-on-evidence-v1","last_clock":"0001-01-01T00:00:00Z","records":{},"cases":{}}');
