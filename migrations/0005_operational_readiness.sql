UPDATE mantik_state SET payload=jsonb_set(payload,'{operations}',
 COALESCE(NULLIF(payload->'operations','null'::jsonb),'{"last_clock":"0001-01-01T00:00:00Z","workers":{}}'::jsonb));
ALTER TABLE mantik_state ADD CONSTRAINT mantik_state_requires_operations
 CHECK(payload ? 'operations' AND jsonb_typeof(payload->'operations')='object');
ALTER TABLE mantik_events DROP CONSTRAINT mantik_event_requires_source;
ALTER TABLE mantik_events ADD CONSTRAINT mantik_event_requires_source
 CHECK(observation_id IS NOT NULL OR coalesce(payload->>'timer_id','')<>'' OR coalesce(payload->>'action_id','')<>'' OR coalesce(payload->>'exception_id','')<>'');
