-- Sealed allocation journal records. The active journal's checkpoint chains
-- every row in sequence order; routine replay starts from that checkpoint.
CREATE TABLE IF NOT EXISTS mantik_owned_allocation_archive (
    sequence integer PRIMARY KEY CHECK (sequence > 0),
    command_id text NOT NULL UNIQUE,
    payload bytea NOT NULL
);
COMMENT ON TABLE mantik_owned_allocation_archive IS 'Sealed owned allocation journal records, chained by the active journal checkpoint; audit re-executes them';
COMMENT ON TABLE mantik_owned_allocations IS 'Mantik owned allocation comparisons through v16 owner entry execution with checkpoints sealing archived records; paper fills only, no exits, broker execution or qualification';
