UPDATE mantik_feed_collectors
 SET payload=jsonb_set(payload,'{admissions}','{}'::jsonb)
 WHERE NOT (payload ? 'admissions');
DO $$ BEGIN
 IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname='mantik_collectors_require_admissions'
   AND conrelid='mantik_feed_collectors'::regclass) THEN
  ALTER TABLE mantik_feed_collectors ADD CONSTRAINT mantik_collectors_require_admissions
   CHECK(payload ? 'admissions' AND jsonb_typeof(payload->'admissions')='object');
 END IF;
END $$;
