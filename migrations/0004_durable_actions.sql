UPDATE mantik_state SET payload=jsonb_set(payload,'{actions}',
 COALESCE(NULLIF(payload->'actions','null'::jsonb),'{"last_clock":"0001-01-01T00:00:00Z","items":{}}'::jsonb));
ALTER TABLE mantik_state ADD CONSTRAINT mantik_state_requires_actions
 CHECK(payload ? 'actions' AND jsonb_typeof(payload->'actions')='object');
CREATE TABLE mantik_fixture_effects (
 idempotency_key text PRIMARY KEY,
 payload jsonb NOT NULL
);
ALTER TABLE mantik_events DROP CONSTRAINT mantik_event_requires_source;
ALTER TABLE mantik_events ADD CONSTRAINT mantik_event_requires_source
 CHECK(observation_id IS NOT NULL OR coalesce(payload->>'timer_id','')<>'' OR coalesce(payload->>'action_id','')<>'');
