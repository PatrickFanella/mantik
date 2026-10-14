-- Collector declarations are separate from the fixture/research aggregate.
-- Existing aggregate writers cannot discard ownership or its command receipts.
CREATE TABLE IF NOT EXISTS mantik_feed_collectors (
 singleton boolean PRIMARY KEY CHECK(singleton),
 payload jsonb NOT NULL CHECK(payload ? 'entries' AND payload ? 'commands'
   AND jsonb_typeof(payload->'entries') = 'object'
   AND jsonb_typeof(payload->'commands') = 'object'
   AND payload ? 'last_clock')
);
INSERT INTO mantik_feed_collectors(singleton,payload)
 VALUES(true,'{"last_clock":"0001-01-01T00:00:00Z","entries":{},"commands":{}}')
 ON CONFLICT(singleton) DO NOTHING;
