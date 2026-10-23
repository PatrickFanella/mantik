-- Fence earlier binaries before new allocation payloads are written. No history is rewritten.
COMMENT ON TABLE mantik_owned_allocations IS 'Owner-captured v6 evaluation declaration context; schema 48; no admission or execution authority';
