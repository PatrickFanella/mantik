-- Fence writers whose checkpoints keep settled reservations in sealed ledgers.
COMMENT ON TABLE mantik_owned_allocation_archive IS 'Sealed owned allocation journal records, chained by the active journal checkpoint whose ledgers drop settled reservations; audit re-executes them';
