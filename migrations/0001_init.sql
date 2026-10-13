CREATE TABLE IF NOT EXISTS worker_runs (
  id bigserial PRIMARY KEY,
  status text NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now()
);
