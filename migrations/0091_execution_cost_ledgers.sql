-- Fence writers that start v16 policy ledgers without the cost ledger schema.
COMMENT ON TABLE mantik_owned_allocations IS 'Mantik owned allocation comparisons through v16 owner entry execution on cost ledgers and execution steps; paper fills only, no exits, broker execution or qualification';
