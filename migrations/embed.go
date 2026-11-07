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

//go:embed 0056_source_observation_clock.sql
var SourceObservationClock string

//go:embed 0057_decision_source_membership.sql
var DecisionSourceMembership string

//go:embed 0058_outcome_source_membership.sql
var OutcomeSourceMembership string

//go:embed 0059_decision_review_sources.sql
var DecisionReviewSources string

//go:embed 0060_distinct_evaluation_variants.sql
var DistinctEvaluationVariants string

//go:embed 0061_known_source_leakage_assessments.sql
var KnownSourceLeakageAssessments string

//go:embed 0062_assessment_failure_bindings.sql
var AssessmentFailureBindings string

//go:embed 0063_execution_safety_bindings.sql
var ExecutionSafetyBindings string

//go:embed 0064_admission_safety_bindings.sql
var AdmissionSafetyBindings string

//go:embed 0065_owned_proposal_model_expiry.sql
var OwnedProposalModelExpiry string

//go:embed 0066_owned_excluded_assessment_failure.sql
var OwnedExcludedAssessmentFailure string

//go:embed 0067_social_research_requests.sql
var SocialResearchRequests string

//go:embed 0068_social_research_source_applicability.sql
var SocialResearchSourceApplicability string

//go:embed 0069_retained_social_research_selection.sql
var RetainedSocialResearchSelection string

//go:embed 0070_retained_copy_research_selection.sql
var RetainedCopyResearchSelection string

//go:embed 0071_corporate_action_source_custody.sql
var CorporateActionSourceCustody string

//go:embed 0072_evaluation_corporate_source_cuts.sql
var EvaluationCorporateSourceCuts string

//go:embed 0073_journal_corporate_sources.sql
var JournalCorporateSources string

//go:embed 0074_predeclared_corporate_selection.sql
var PredeclaredCorporateSelection string

//go:embed 0075_observed_portfolio_assessments.sql
var ObservedPortfolioAssessments string

//go:embed 0076_predeclared_holdout_assessments.sql
var PredeclaredHoldoutAssessments string

//go:embed 0077_observed_benchmark_assessments.sql
var ObservedBenchmarkAssessments string

//go:embed 0078_social_research_dispatch.sql
var SocialResearchDispatch string

//go:embed 0079_social_research_worker_health.sql
var SocialResearchWorkerHealth string

//go:embed 0080_native_feature_causality.sql
var NativeFeatureCausality string

//go:embed 0081_disjoint_sample_design.sql
var DisjointSampleDesign string

//go:embed 0082_search_history_multiplicity.sql
var SearchHistoryMultiplicity string

//go:embed 0083_corporate_reference_sources.sql
var CorporateReferenceSources string

//go:embed 0084_corporate_coverage_assessments.sql
var CorporateCoverageAssessments string

//go:embed 0085_total_return_benchmarks.sql
var TotalReturnBenchmarks string

//go:embed 0086_threshold_sensitivity.sql
var ThresholdSensitivity string

//go:embed 0087_holdout_sign_tests.sql
var HoldoutSignTests string

//go:embed 0088_owned_paper_eligibility.sql
var OwnedPaperEligibility string

//go:embed 0089_drawdown_gate.sql
var DrawdownGate string

//go:embed 0090_paper_entry_execution.sql
var PaperEntryExecution string

//go:embed 0091_execution_cost_ledgers.sql
var ExecutionCostLedgers string
