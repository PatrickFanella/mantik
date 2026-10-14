-- Additive payload migration. Retain the original aggregate and all evidence.
UPDATE mantik_state
SET payload = jsonb_set(payload, '{routing}',
    COALESCE(NULLIF(payload->'routing', 'null'::jsonb),
        '{"definitions":[],"runtime":{},"cases":[],"inputs":{}}'::jsonb));

-- An older running writer must fail rather than discard unknown routing data.
ALTER TABLE mantik_state ADD CONSTRAINT mantik_state_requires_routing
CHECK (payload ? 'routing' AND jsonb_typeof(payload->'routing') = 'object');
