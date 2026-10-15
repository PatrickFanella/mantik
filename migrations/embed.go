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

// AttributedRouting fences writers that cannot retain market-input provenance.
//
//go:embed 0006_attributed_routing.sql
var AttributedRouting string

// ResearchReceiver installs persistent identity for fenced research submission.
//
//go:embed 0007_research_receiver.sql
var ResearchReceiver string

// FeedCollectors retains immutable registrations and fenced ownership receipts.
//
//go:embed 0008_feed_collectors.sql
var FeedCollectors string

//go:embed 0009_collector_admissions.sql
var CollectorAdmissions string

//go:embed 0010_feed_evidence.sql
var FeedEvidence string

//go:embed 0011_news_cases.sql
var NewsCases string

//go:embed 0012_news_reviews.sql
var NewsReviews string
