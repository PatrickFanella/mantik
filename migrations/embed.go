package migrations

import _ "embed"

//go:embed 0001_bootstrap.sql
var Bootstrap string

//go:embed 0002_trigger_routing.sql
var TriggerRouting string
