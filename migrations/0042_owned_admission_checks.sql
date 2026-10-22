-- Fence older writers that cannot preserve v2 admission readiness fields.
COMMENT ON TABLE mantik_owned_allocations IS 'Versioned service-owned admission readiness and allocation deferrals; no execution authority';
