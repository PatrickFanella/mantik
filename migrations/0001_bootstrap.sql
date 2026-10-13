CREATE TABLE mantik_state (
    id text PRIMARY KEY CHECK (id = 'demo'),
    payload jsonb NOT NULL
);
CREATE TABLE mantik_observations (
    id text PRIMARY KEY,
    payload jsonb NOT NULL,
    receipt jsonb NOT NULL
);
CREATE TABLE mantik_events (
    sequence bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    id text UNIQUE NOT NULL,
    observation_id text NOT NULL REFERENCES mantik_observations(id),
    payload jsonb NOT NULL
);
