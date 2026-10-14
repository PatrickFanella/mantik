UPDATE mantik_state SET payload=jsonb_set(payload,'{routing,input_contract}','"attributed-routing-v1"'::jsonb);
ALTER TABLE mantik_state ADD CONSTRAINT mantik_state_requires_input_contract
 CHECK(COALESCE(payload->'routing'->>'input_contract','')='attributed-routing-v1');
