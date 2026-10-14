package migrations

import _ "embed"

//go:embed 0001_bootstrap.sql
var Bootstrap string

//go:embed 0002_trigger_routing.sql
var TriggerRouting string

// IndependentTimers installs durable clock and audit-source contracts.
//
//go:embed 0003_independent_timers.sql
var IndependentTimers string
