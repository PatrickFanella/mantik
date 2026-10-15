CREATE TABLE IF NOT EXISTS mantik_feed_evidence (
 registration_id text NOT NULL,
 capture_key text NOT NULL,
 payload jsonb NOT NULL,
 PRIMARY KEY(registration_id,capture_key),
 CHECK(jsonb_typeof(payload)='object')
);
