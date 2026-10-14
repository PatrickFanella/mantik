UPDATE mantik_state SET payload=jsonb_set(payload,'{routing,timers}',
 COALESCE(NULLIF(payload#>'{routing,timers}','null'::jsonb),
 '{"last_clock":"0001-01-01T00:00:00Z","jobs":{},"exceptions":{}}'::jsonb));
ALTER TABLE mantik_state ADD CONSTRAINT mantik_state_requires_timers
 CHECK (payload#>'{routing,timers}' IS NOT NULL AND jsonb_typeof(payload#>'{routing,timers}')='object');
ALTER TABLE mantik_events ALTER COLUMN observation_id DROP NOT NULL;
ALTER TABLE mantik_events ADD CONSTRAINT mantik_event_requires_source
 CHECK (observation_id IS NOT NULL OR coalesce(payload->>'timer_id','') <> '');
