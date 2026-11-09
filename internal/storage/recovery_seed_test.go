package storage

import (
	"context"
	"encoding/json"
	"errors"
	"git.subcult.tv/subculture-collective/mantik/internal/socialwatch"
	"git.subcult.tv/subculture-collective/mantik/internal/workerplan"
	"net/url"
	"os"
	"reflect"
	"regexp"
	"testing"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/domain"
	"git.subcult.tv/subculture-collective/mantik/internal/feedcollector"
	"git.subcult.tv/subculture-collective/mantik/internal/paperfill"
	"git.subcult.tv/subculture-collective/mantik/internal/paperstrategy"
)

// The integration runner owns this fresh database and subsequently dumps and
// restores it before removing its disposable server. No application worker runs.
func TestPostgresContractsRecoverySeed(t *testing.T) {
	if os.Getenv("MANTIK_RECOVERY_SEED") != "1" || os.Getenv("MANTIK_TEST_DATABASE_URL") == "" {
		t.Skip("disposable recovery integration only")
	}
	ctx := context.Background()
	s, err := OpenPostgres(ctx, os.Getenv("MANTIK_TEST_DATABASE_URL"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	copyRecordContracts(t, s)
	at := time.Now().UTC()
	clock := func() time.Time { return at }
	account := policyAccount(t, s, "owner")
	d := strategyDefinition("baseline-v1")
	d.InputEvidenceClass = "prospective_market_unqualified"
	strategy, err := s.RegisterPaperStrategy(ctx, paperstrategy.Command{ReceiverID: account.ReceiverID, Definition: d}, clock)
	if err != nil {
		t.Fatal(err)
	}
	policy := policyCommand(account, "policy")
	policy.Policy.Strategies[0].ArtifactSHA256 = strategy.Record.DefinitionSHA256
	policy.Policy.Symbols[0].ID = "SPY"
	policy.ExecutionModel = &paperfill.Model{Method: paperfill.Method, SlippageBPS: 10, FeeBPS: 20, CommissionPerFillCents: 3, LiquidityBPS: 5000, LatencyMillis: 1000, MaxQuoteAgeSeconds: 60}
	registeredPolicy, err := s.RegisterAllocationPolicy(ctx, policy, clock)
	if err != nil {
		t.Fatal(err)
	}
	reg := feedcollector.Registration{ID: "spy-iex", Provider: "alpaca", Feed: "iex", Symbol: "SPY", EvidenceClass: d.InputEvidenceClass, JournalID: "journal-one", AccessRef: "access-one", LeaseSeconds: 300}
	for _, c := range []feedcollector.Command{{RequestID: "register", Operation: "register", RegistrationID: reg.ID, Registration: &reg}, {RequestID: "claim", Operation: "claim", RegistrationID: reg.ID, OwnerID: "fixture-worker"}} {
		if _, err = s.FeedCollectorCommand(ctx, account.ReceiverID, c, clock); err != nil {
			t.Fatal(err)
		}
	}
	input := collectorInput(at, "recovery-quote")
	if _, err = s.SubmitCollectorResearch(ctx, account.ReceiverID, feedcollector.Fence{RegistrationID: reg.ID, OwnerID: "fixture-worker", Generation: 1}, input, clock); err != nil {
		t.Fatal(err)
	}
	// A pure storage fixture evaluator, not the service's economic admission issuer.
	eval := recoveryFixtureDecisionEvaluator
	decision, err := s.FinalizePaperDecision(ctx, PaperDecisionCommand{ID: "recovery-decision", ReceiverID: account.ReceiverID, StrategyID: d.ID, StrategySHA256: strategy.Record.SHA256, RegistrationID: reg.ID}, clock, eval)
	if err != nil {
		t.Fatal(err)
	}
	command := OwnedAllocationCommand{ID: "recovery-comparison", ReceiverID: account.ReceiverID, Decisions: []OwnedDecisionPin{{ID: decision.Record.Command.ID, SHA256: decision.Record.SHA256}}}
	if _, err = s.CompareOwnedAllocation(ctx, command, func() time.Time { return at.Add(time.Second) }, eval); err != nil {
		t.Fatal(err)
	}
	valid, err := s.OwnedAllocations(ctx, eval)
	if err != nil || len(valid.Records) != 1 || len(valid.Ledgers) != 1 || valid.Records[0].AdmissionChecks[0].AdmissionAvailable || valid.Records[0].ExecutionAvailable || valid.Records[0].QualificationCredit {
		t.Fatal("invalid recovery seed", valid, err)
	}
	// Retain actual evaluation owners in the exported snapshot. This storage
	// fixture supplies no measured outcomes or economic admission authority.
	stress := *policy.ExecutionModel
	stress.FeeBPS++
	plan := PaperEvaluationDeclarationCommand{ID: "recovery-evaluation-plan", ReceiverID: account.ReceiverID, Recipe: PaperEvaluationRecipe, PolicyID: policy.Policy.ID, PolicySHA256: registeredPolicy.Record.SHA256, Plan: PaperEvaluationPlan{
		CorporateSources: []PaperEvaluationCorporateSourcePlan{{Symbol: "SPY", JournalID: "journal-manufactured", RootCollectionID: "acquired-recovery-corporate-source", AccessRef: "manufactured-access", Start: "2026-10-14", End: "2026-10-14"}},
		FamilyID:         "recovery-evaluation-family", Variants: []PaperEvaluationVariant{{ID: d.ID, SHA256: strategy.Record.SHA256}},
		WindowStart: at.Add(time.Hour), WindowEnd: at.Add(2 * time.Hour), OutcomeDeadline: at.Add(26 * time.Hour),
		MinIndependentDecisions: 30, MinimumNetExcessCents: 0, MaximumDrawdownCents: 1000, AdmissionTTLSeconds: 3600,
		Benchmarks: []string{"cash_no_action", "passive_same_symbol"}, StressModel: stress,
	}}
	declaration, err := s.DeclarePaperEvaluation(ctx, plan, func() time.Time { return at.Add(2 * time.Second) })
	if err != nil {
		t.Fatal("evaluation recovery declaration seed", err)
	}
	assessmentCommand := PaperEvaluationAssessmentCommand{ID: "recovery-evaluation-assessment", ReceiverID: account.ReceiverID, DeclarationID: plan.ID, DeclarationSHA256: declaration.Record.SHA256}
	corporate, acquisition := journalCorporateSourceFixture(account.ReceiverID, "recovery-corporate-source")
	if _, err := s.RetainJournalCorporateActionSource(ctx, corporate, acquisition, func() time.Time { return plan.Plan.OutcomeDeadline }); err != nil {
		t.Fatal("corporate source recovery seed", err)
	}
	assessment, err := s.AssessPaperEvaluation(ctx, assessmentCommand, func() time.Time { return plan.Plan.OutcomeDeadline.Add(time.Nanosecond) }, eval, recoveryFixtureOutcomeEvaluator)
	if err != nil || assessment.Record.Schema != "mantik-owned-paper-evaluation-assessment-v5" || assessment.Record.Status != "blocked" || assessment.Record.EvaluationComplete || assessment.Record.AdmissionAvailable || assessment.Record.ExecutionAvailable || assessment.Record.QualificationCredit {
		t.Fatal("evaluation recovery assessment seed", assessment, err)
	}

	// Retain an actual owner research request for snapshot/restore replay.
	history, err := s.SocialHistory(ctx)
	if err != nil {
		t.Fatal(err)
	}
	researchAt := time.Now().UTC()
	for _, entry := range history.History {
		if !researchAt.After(entry.At) {
			researchAt = entry.At.Add(time.Nanosecond)
		}
	}
	researchClock := func() time.Time { return researchAt }
	watch := socialwatch.PolicyCommand{ID: "recovery-research-policy", ReceiverID: history.ReceiverID, Policy: socialwatch.Policy{ID: "recovery-research-watch", Provider: "recorded", AccountID: "recovery-research-source", Symbols: []string{"SPY"}, Behavior: "research", MaxObservationDelaySeconds: 60, Enabled: true}}
	registered, err := s.ApplySocialCommand(ctx, socialwatch.Command{Policy: &watch}, researchClock)
	if err != nil {
		t.Fatal(err)
	}
	original := socialwatch.Original{Schema: "mantik-social-recording-v1", Source: socialwatch.SourceRef{Provider: "recorded", AccountID: "recovery-research-source", PostID: "post"}, Revision: 1, Kind: "post", OccurredAt: researchAt, ObservedAt: researchAt, Text: "SPY recovery research source", Symbols: []string{"SPY"}}
	raw, _ := json.Marshal(original)
	capture := socialwatch.CaptureCommand{ID: "recovery-research-recording", ReceiverID: history.ReceiverID, WatchID: watch.Policy.ID, PolicySHA256: registered.Entry.Policy.SHA256, EvidenceClass: "recorded_social_unqualified", Original: string(raw), OriginalSHA256: workerplan.Digest(raw), RequestResearch: false}
	if _, err = s.ApplySocialCommand(ctx, socialwatch.Command{Capture: &capture}, researchClock); err != nil {
		t.Fatal(err)
	}
	historyForSelection, e := s.SocialHistory(ctx)
	if e != nil {
		t.Fatal(e)
	}
	sourceForSelection := historyForSelection.History[len(historyForSelection.History)-1].Capture
	selected := socialwatch.ResearchCommand{ID: "recovery-social-selection", ReceiverID: history.ReceiverID, CaptureID: capture.ID, CaptureSHA256: sourceForSelection.SHA256}
	if _, e = s.ApplySocialCommand(ctx, socialwatch.Command{Research: &selected}, researchClock); e != nil {
		t.Fatal(e)
	}

	original.Schema = socialwatch.CopyRecordingSchema
	original.Source.PostID = "recovery-copy-source"
	original.Activity = &socialwatch.Activity{Kind: "observed_fill", State: "reported", SourceSymbol: "SPY.US", Symbol: "SPY", MappingVersion: "asserted-v1", Side: "buy", QuantityUnits: 5, QuantityUnitClaim: "shares", PriceCents: 10000}
	raw, _ = json.Marshal(original)
	capture.ID = "recovery-copy-recording"
	capture.EvidenceClass = "recorded_copy_unqualified"
	capture.Original = string(raw)
	capture.OriginalSHA256 = workerplan.Digest(raw)
	copySource, e := s.ApplySocialCommand(ctx, socialwatch.Command{Capture: &capture}, researchClock)
	if e != nil {
		t.Fatal(e)
	}
	selected.ID = "recovery-copy-selection"
	selected.CaptureID = capture.ID
	selected.CaptureSHA256 = copySource.Entry.Capture.SHA256
	if _, e = s.ApplySocialCommand(ctx, socialwatch.Command{Research: &selected}, researchClock); e != nil {
		t.Fatal(e)
	}
	if got, err := s.SocialHistory(ctx); err != nil || len(got.ResearchRequests) != 2 {
		t.Fatal("research recovery seed", got, err)
	}

	history, err = s.SocialHistory(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, request := range history.ResearchRequests {
		dispatched, e := s.DispatchSocialResearch(ctx, SocialResearchDispatchCommand{ReceiverID: history.ReceiverID, RequestID: request.ID, RequestSHA256: request.SHA256}, researchClock)
		if e != nil || len(dispatched.Record.Links) != 1 || dispatched.Record.AdmissionAvailable {
			t.Fatal("dispatch recovery seed", dispatched, e)
		}
	}

	// Include populated archive/index/checkpoint owners in the exported snapshot.
	// This forward record remains a storage fixture with no measured return.
	custodyAt := assessment.Record.AssessedAt.Add(time.Nanosecond)
	if _, err := s.AssessPaperOutcome(ctx, PaperOutcomeCommand{ID: "recovery-outcome", ReceiverID: account.ReceiverID, DecisionID: decision.Record.Command.ID, DecisionSHA256: decision.Record.SHA256}, func() time.Time { return custodyAt }, eval, recoveryFixtureOutcomeEvaluator); err != nil {
		t.Fatal("paper custody outcome recovery seed", err)
	}
	custodyAt = custodyAt.Add(time.Nanosecond)
	archive, err := s.ArchivePaperFeed(ctx, account.ReceiverID, func() time.Time { return custodyAt }, func(FeedEvidenceSnapshot, time.Time) error { return nil }, eval, recoveryFixtureOutcomeEvaluator)
	if err != nil || archive.PaperCustody == nil || archive.PaperCustody.Header.Decisions != 1 || archive.PaperCustody.Header.Outcomes != 1 {
		t.Fatal("paper custody recovery seed", archive, err)
	}
	retirement, err := s.RotateFeed(ctx, account.ReceiverID, func() time.Time { t.Fatal("same-source retirement sampled clock"); return time.Time{} }, func(FeedEvidenceSnapshot, time.Time) error { return nil }, eval, recoveryFixtureOutcomeEvaluator)
	if err != nil || retirement.Retention == nil || retirement.Retention.CommandsRetired != 1 {
		t.Fatal("feed retirement recovery seed", retirement, err)
	}
	custodyAt = custodyAt.Add(time.Nanosecond)
	postRetirement, err := s.FinalizePaperDecision(ctx, PaperDecisionCommand{ID: "recovery-retired-feed-decision", ReceiverID: account.ReceiverID, StrategyID: d.ID, StrategySHA256: strategy.Record.SHA256, RegistrationID: reg.ID}, func() time.Time { return custodyAt }, eval)
	if err != nil || postRetirement.Record.Schema != paperDecisionSchemaV4 {
		t.Fatal("retired feed paper recovery seed", postRetirement, err)
	}
	custodyAt = custodyAt.Add(time.Nanosecond)
	if outcome, err := s.AssessPaperOutcome(ctx, PaperOutcomeCommand{ID: "recovery-retired-feed-outcome", ReceiverID: account.ReceiverID, DecisionID: postRetirement.Record.Command.ID, DecisionSHA256: postRetirement.Record.SHA256}, func() time.Time { return custodyAt }, eval, recoveryFixtureOutcomeEvaluator); err != nil || outcome.Record.Schema != paperOutcomeSchemaV4 {
		t.Fatal("retired feed outcome recovery seed", outcome, err)
	}
	if audit, err := s.AuditPaperFeed(ctx, eval, recoveryFixtureOutcomeEvaluator); err != nil || audit.Decisions != 2 || audit.Outcomes != 2 {
		t.Fatal("retired feed recovery source replay", audit, err)
	}
	custodyAt = custodyAt.Add(time.Nanosecond)
	if paperArchive, err := s.ArchivePaperRecords(ctx, account.ReceiverID, func() time.Time { return custodyAt }, eval, recoveryFixtureOutcomeEvaluator); err != nil || paperArchive.Header.Decisions != 2 || paperArchive.Header.Outcomes != 2 || paperArchive.Header.Assessments != 1 {
		t.Fatal("paper record archive recovery seed", paperArchive, err)
	}
}

// Shared by source seeding and restore replay. This compiled storage fixture
// evaluator is not an application admission issuer or economic evaluator.
func recoveryFixtureDecisionEvaluator(c PaperDecisionCommand, b PaperDecisionBasis, _ time.Time) (PaperDecisionEvaluation, error) {
	input, ok := b.Feed.Routing.Inputs[collectorInput(b.Strategy.RegisteredAt, "recovery-quote").ID]
	if !ok {
		return PaperDecisionEvaluation{}, ErrPaperDecisionInvalid
	}
	return PaperDecisionEvaluation{Action: "entry_candidate", ReasonCode: "storage_recovery_fixture_only", PriceCents: input.Input.PriceCents, Signal: true, Coverage: domain.MarketCoverageReport{Quote: domain.MarketQuoteCoverage{InputID: input.Input.ID}}}, nil
}

// Recovery retains a waiting outcome to exercise source custody and restore.
// This compiled storage fixture supplies no observed economic result.
func recoveryFixtureOutcomeEvaluator(b PaperOutcomeBasis, _ time.Time) (PaperOutcomeEvaluation, error) {
	return PaperOutcomeEvaluation{Status: "waiting_price", ReasonCode: "storage_recovery_fixture_only", DueAt: b.Decision.FinalizedAt.Add(time.Hour), WindowEnd: b.Decision.FinalizedAt.Add(2 * time.Hour)}, nil
}

func TestPostgresContractsRecoveryReplay(t *testing.T) {
	if os.Getenv("MANTIK_RECOVERY_REPLAY") != "1" {
		t.Skip("explicit disposable restored owner replay only")
	}
	raw := os.Getenv("MANTIK_TEST_DATABASE_URL")
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "postgres" || u.Host != "127.0.0.1:5432" || u.User == nil || u.User.Username() != "mantik" || !regexp.MustCompile(`^/mantik_test_[a-f0-9]{12}_restore$`).MatchString(u.Path) {
		t.Fatal("recovery replay requires runner-owned restored database")
	}
	ctx := context.Background()
	s, err := OpenPostgres(ctx, raw)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	got, err := s.OwnedAllocations(ctx, recoveryFixtureDecisionEvaluator, recoveryFixtureOutcomeEvaluator)
	if os.Getenv("MANTIK_RECOVERY_EXPECT_REJECTION") == "1" {
		if !errors.Is(err, ErrOwnedAllocationInvalid) || !reflect.DeepEqual(got, OwnedAllocationCatalog{}) {
			t.Fatal("corrupt restored owner binding not rejected", err)
		}
		return
	}
	if err != nil || len(got.Records) != 1 || len(got.Ledgers) != 1 || len(got.Capital) != 1 {
		t.Fatal("restored owner replay", err)
	}
	r := got.Records[0]
	if r.Command.ID != "recovery-comparison" || len(r.ExecutionSafety) != 1 || len(r.AdmissionChecks) != 1 || r.AdmissionChecks[0].AdmissionAvailable || r.ExecutionAvailable || r.QualificationCredit || got.Capital[0].AvailableCashCents != got.Capital[0].Account.Config.OpeningCashCents || len(got.Capital[0].Positions) != 0 || len(got.Capital[0].Orders) != 0 {
		t.Fatal("restored owner altered deferral-only capital", got)
	}
	retry, err := s.CompareOwnedAllocation(ctx, r.Command, func() time.Time { t.Fatal("restore retry sampled a clock"); return time.Time{} }, recoveryFixtureDecisionEvaluator, recoveryFixtureOutcomeEvaluator)
	if err != nil || !retry.Duplicate || !reflect.DeepEqual(retry.Record, r) {
		t.Fatal("restored retry changed retained receipt", err)
	}
	assertRestoredEvaluationOwner(t, s, got.ReceiverID)
	assertRestoredCorporateSource(t, s, got.ReceiverID)
	assertRestoredPaperFeedCheckpoint(t, s, got.ReceiverID)
	assertRestoredPaperRecordArchive(t, s, got.ReceiverID)
	history, err := s.SocialHistory(ctx)
	if err != nil || history.ReceiverID != got.ReceiverID || len(history.ResearchRequests) != 2 || history.ResearchRequests[0].DispatchAvailable || history.ResearchRequests[0].AdmissionAvailable || history.ResearchRequests[0].ExecutionAvailable || history.ResearchRequests[0].QualificationCredit {
		t.Fatal("restored social owner", err)
	}
	for _, request := range history.ResearchRequests {
		if request.CaptureID != "recovery-research-recording" && request.CaptureID != "recovery-copy-recording" || request.DispatchAvailable || request.AdmissionAvailable || request.ExecutionAvailable || request.QualificationCredit {
			t.Fatal("restored research grant or unknown source", request)
		}
	}

	dispatches, err := s.SocialResearchDispatches(ctx)
	if err != nil || len(dispatches.Records) != 2 {
		t.Fatal("restored research dispatches", dispatches, err)
	}
	for _, record := range dispatches.Records {
		if record.ProviderAccessVerified || record.IndependentEvidenceCredit || record.AdmissionAvailable || record.ExecutionAvailable || record.QualificationCredit || len(record.Links) != 1 {
			t.Fatal("restored dispatch gained authority", record)
		}
		duplicate, e := s.DispatchSocialResearch(ctx, record.Command, func() time.Time { panic("restore dispatch retry clock") })
		if e != nil || !duplicate.Duplicate || !reflect.DeepEqual(duplicate.Record, record) {
			t.Fatal("restored dispatch retry", duplicate, e)
		}
	}
	t.Logf("social dispatch restore preserved receipts=%s", workerplan.Hash(dispatches))
	t.Logf("owner replay preserved allocation=%s safety=%s ledger=%s social=%s", r.SHA256, r.ExecutionSafety[0].SHA256, r.LedgerSHA256, workerplan.Hash(history))
}

// This runs only against the exact runner-owned restored database. It verifies
// real replay and retries, then rejects a self-rehashed grant substitution and
// restores the original payload bytes before the outer snapshot identity check.
func assertRestoredEvaluationOwner(t *testing.T, s *Postgres, owner string) {
	t.Helper()
	ctx := context.Background()
	plans, err := s.PaperEvaluationDeclarations(ctx)
	if err != nil || plans.ReceiverID != owner || len(plans.Records) != 1 {
		t.Fatal("restored evaluation plan owner", plans, err)
	}
	plan := plans.Records[0]
	assessments, err := s.PaperEvaluationAssessments(ctx, recoveryFixtureDecisionEvaluator, recoveryFixtureOutcomeEvaluator)
	if err != nil || assessments.ReceiverID != owner || len(assessments.Records) != 1 {
		t.Fatal("restored evaluation assessment owner", assessments, err)
	}
	record := assessments.Records[0]
	if plan.Command.ID != "recovery-evaluation-plan" || record.Command.ID != "recovery-evaluation-assessment" || record.Command.DeclarationID != plan.Command.ID || record.Command.DeclarationSHA256 != plan.SHA256 || record.Evidence.Declaration.SHA256 != plan.SHA256 || record.Schema != "mantik-owned-paper-evaluation-assessment-v5" || record.Status != "blocked" || record.EvaluationComplete || record.AdmissionAvailable || record.ExecutionAvailable || record.QualificationCredit {
		t.Fatal("restored evaluation lost source binding or gained authority", plan, record)
	}
	if record.CorporateSourceCut == nil || len(record.CorporateSourceCut.Sources) != 1 || record.CorporateSourceCut.Sources[0].ID != "recovery-corporate-source" {
		t.Fatal("restored assessment lost actual corporate source custody cut", record)
	}
	assertCorporateCutNoGrant(t, record)
	chain, err := s.PaperEvaluationCorporatePageChain(ctx, record.Command.ID, "recovery-corporate-source", recoveryFixtureDecisionEvaluator, recoveryFixtureOutcomeEvaluator)
	if err != nil || chain.Status != "next_page_unavailable_at_cut" || len(chain.Pages) != 1 || chain.RootSourceSHA256 != record.CorporateSourceCut.Sources[0].SHA256 || chain.Pages[0].Source.AcquisitionSHA256 == "" || chain.CoverageComplete || chain.EconomicApplicationReady || chain.AdmissionAvailable || chain.ExecutionAvailable || chain.QualificationCredit {
		t.Fatal("restored chain lost acquisition binding or granted authority", chain, err)
	}
	selection, err := s.PaperEvaluationCorporateSelection(ctx, record.Command.ID, recoveryFixtureDecisionEvaluator, recoveryFixtureOutcomeEvaluator)
	if err != nil || len(selection.Rows) != 1 || selection.Rows[0].Plan.RootCollectionID != "acquired-recovery-corporate-source" || selection.Rows[0].Chain == nil || selection.Rows[0].PredeclaredChainResolved || selection.CoverageComplete || selection.EconomicApplicationReady || selection.AdmissionAvailable || selection.ExecutionAvailable || selection.QualificationCredit {
		t.Fatal("restored selection lost predeclaration or granted authority", selection, err)
	}
	noClock := func() time.Time { t.Fatal("restored evaluation retry sampled clock"); return time.Time{} }
	retriedPlan, err := s.DeclarePaperEvaluation(ctx, plan.Command, noClock)
	if err != nil || !retriedPlan.Duplicate || !reflect.DeepEqual(retriedPlan.Record, plan) {
		t.Fatal("restored plan retry changed receipt", err)
	}
	retried, err := s.AssessPaperEvaluation(ctx, record.Command, noClock, recoveryFixtureDecisionEvaluator, recoveryFixtureOutcomeEvaluator)
	if err != nil || !retried.Duplicate || !reflect.DeepEqual(retried.Record, record) {
		t.Fatal("restored assessment retry changed receipt", err)
	}
	var original []byte
	if err := s.pool.QueryRow(ctx, "SELECT payload FROM mantik_paper_evaluation_assessments WHERE id=$1", record.Command.ID).Scan(&original); err != nil {
		t.Fatal(err)
	}
	func() {
		defer func() {
			if _, err := s.pool.Exec(ctx, "UPDATE mantik_paper_evaluation_assessments SET payload=$1 WHERE id=$2", original, record.Command.ID); err != nil {
				t.Fatal("restore original assessment bytes", err)
			}
		}()
		damaged := clone(record)
		damaged.AdmissionAvailable = true
		damaged.SHA256 = ""
		damaged.SHA256 = workerplan.Hash(damaged)
		raw, err := json.Marshal(damaged)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := s.pool.Exec(ctx, "UPDATE mantik_paper_evaluation_assessments SET payload=$1 WHERE id=$2", raw, record.Command.ID); err != nil {
			t.Fatal(err)
		}
		rejected, err := s.PaperEvaluationAssessments(ctx, recoveryFixtureDecisionEvaluator, recoveryFixtureOutcomeEvaluator)
		if !errors.Is(err, ErrPaperEvaluationAssessmentInvalid) || !reflect.DeepEqual(rejected, PaperEvaluationAssessmentCatalog{}) {
			t.Fatal("restored rehashed assessment grant accepted", rejected, err)
		}
	}()
	again, err := s.PaperEvaluationAssessments(ctx, recoveryFixtureDecisionEvaluator, recoveryFixtureOutcomeEvaluator)
	if err != nil || !reflect.DeepEqual(again, assessments) {
		t.Fatal("repaired restored assessment owner changed", err)
	}
	t.Logf("restored evaluation plan=%s assessment=%s; clock-free retries and self-rehashed grant rejection passed", plan.SHA256, record.SHA256)
}
