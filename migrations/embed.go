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

//go:embed 0013_hypothesis_publications.sql
var HypothesisPublications string

//go:embed 0014_hypothesis_trials.sql
var HypothesisTrials string

//go:embed 0015_trial_attempt_intents.sql
var TrialAttemptIntents string

//go:embed 0016_native_trial_runs.sql
var NativeTrialRuns string

//go:embed 0017_external_trial_jobs.sql
var ExternalTrialJobs string

//go:embed 0018_external_trial_attempts.sql
var ExternalTrialAttempts string

//go:embed 0019_review_profiles.sql
var ReviewProfiles string

//go:embed 0020_review_hypothesis_inventories.sql
var ReviewHypothesisInventories string

//go:embed 0021_news_extractions.sql
var NewsExtractions string

//go:embed 0022_reviewed_extraction_admissions.sql
var ReviewedExtractionAdmissions string

//go:embed 0023_local_extraction_captures.sql
var LocalExtractionCaptures string

//go:embed 0024_local_capture_admissions.sql
var LocalCaptureAdmissions string

//go:embed 0025_switchyard_extraction_captures.sql
var SwitchyardExtractionCaptures string

//go:embed 0026_switchyard_capture_admissions.sql
var SwitchyardCaptureAdmissions string

//go:embed 0027_paper_accounts.sql
var PaperAccounts string

//go:embed 0028_paper_ledger_rehearsals.sql
var PaperLedgerRehearsals string

//go:embed 0029_fixture_capital.sql
var FixtureCapital string

//go:embed 0030_candidate_registrations.sql
var CandidateRegistrations string

//go:embed 0031_candidate_worker_intents.sql
var CandidateWorkerIntents string

//go:embed 0032_candidate_worker_outcomes.sql
var CandidateWorkerOutcomes string

//go:embed 0033_candidate_generation_intents.sql
var CandidateGenerationIntents string

//go:embed 0034_candidate_generation_outcomes.sql
var CandidateGenerationOutcomes string

//go:embed 0035_generated_candidate_links.sql
var GeneratedCandidateLinks string

//go:embed 0036_allocation_policies.sql
var AllocationPolicies string

//go:embed 0037_paper_strategies.sql
var PaperStrategies string

//go:embed 0038_paper_decisions.sql
var PaperDecisions string

//go:embed 0039_owned_allocations.sql
var OwnedAllocations string

//go:embed 0040_paper_outcomes.sql
var PaperOutcomes string

//go:embed 0041_social_watches.sql
var SocialWatches string

//go:embed 0042_owned_admission_checks.sql
var OwnedAdmissionChecks string

//go:embed 0043_owned_execution_costs.sql
var OwnedExecutionCosts string

//go:embed 0044_owned_protective_funding.sql
var OwnedProtectiveFunding string

//go:embed 0045_decision_reviews.sql
var DecisionReviews string

//go:embed 0046_paper_evaluation_declarations.sql
var PaperEvaluationDeclarations string

//go:embed 0047_owned_admission_bindings.sql
var OwnedAdmissionBindings string

//go:embed 0048_owned_evaluation_context.sql
var OwnedEvaluationContext string

//go:embed 0049_complete_evaluation_captures.sql
var CompleteEvaluationCaptures string

//go:embed 0050_paper_evaluation_assessments.sql
var PaperEvaluationAssessments string

//go:embed 0051_allocation_assessment_context.sql
var AllocationAssessmentContext string

//go:embed 0052_assessment_decision_readiness.sql
var AssessmentDecisionReadiness string

//go:embed 0053_owned_admission_reviews.sql
var OwnedAdmissionReviews string

//go:embed 0054_native_warmup_assessments.sql
var NativeWarmUpAssessments string

//go:embed 0055_native_calibration_applicability.sql
var NativeCalibrationApplicability string
