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

// DurableActions installs fenced delivery and independent fixture effects.
//
//go:embed 0004_durable_actions.sql
var DurableActions string

// OperationalReadiness adds worker evidence and guards exception metadata.
//
//go:embed 0005_operational_readiness.sql
var OperationalReadiness string
