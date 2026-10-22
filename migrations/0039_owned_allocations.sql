CREATE TABLE mantik_owned_allocations (
 singleton boolean PRIMARY KEY CHECK (singleton),
 payload bytea NOT NULL CHECK (octet_length(payload) <= 8388608)
);
