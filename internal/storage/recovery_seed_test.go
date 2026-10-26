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
	if _, err = s.RegisterAllocationPolicy(ctx, policy, clock); err != nil {
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
	capture := socialwatch.CaptureCommand{ID: "recovery-research-recording", ReceiverID: history.ReceiverID, WatchID: watch.Policy.ID, PolicySHA256: registered.Entry.Policy.SHA256, EvidenceClass: "recorded_social_unqualified", Original: string(raw), OriginalSHA256: workerplan.Digest(raw), RequestResearch: true}
	if _, err = s.ApplySocialCommand(ctx, socialwatch.Command{Capture: &capture}, researchClock); err != nil {
		t.Fatal(err)
	}
	if got, err := s.SocialHistory(ctx); err != nil || len(got.ResearchRequests) != 1 {
		t.Fatal("research recovery seed", got, err)
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
	got, err := s.OwnedAllocations(ctx, recoveryFixtureDecisionEvaluator)
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
	retry, err := s.CompareOwnedAllocation(ctx, r.Command, func() time.Time { t.Fatal("restore retry sampled a clock"); return time.Time{} }, recoveryFixtureDecisionEvaluator)
	if err != nil || !retry.Duplicate || !reflect.DeepEqual(retry.Record, r) {
		t.Fatal("restored retry changed retained receipt", err)
	}
	history, err := s.SocialHistory(ctx)
	if err != nil || history.ReceiverID != got.ReceiverID || len(history.ResearchRequests) != 1 || history.ResearchRequests[0].CaptureID != "recovery-research-recording" || history.ResearchRequests[0].DispatchAvailable || history.ResearchRequests[0].AdmissionAvailable || history.ResearchRequests[0].ExecutionAvailable || history.ResearchRequests[0].QualificationCredit {
		t.Fatal("restored social owner", err)
	}
	t.Logf("owner replay preserved allocation=%s safety=%s ledger=%s social=%s", r.SHA256, r.ExecutionSafety[0].SHA256, r.LedgerSHA256, workerplan.Hash(history))
}
