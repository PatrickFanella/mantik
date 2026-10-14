CREATE TABLE IF NOT EXISTS mantik_research_receiver (
 singleton boolean PRIMARY KEY CHECK(singleton),
 id text NOT NULL CHECK(id ~ '^receiver-[a-f0-9]{32}$')
);
INSERT INTO mantik_research_receiver(singleton,id)
 VALUES(true,'receiver-' || replace(gen_random_uuid()::text,'-',''))
 ON CONFLICT(singleton) DO NOTHING;
