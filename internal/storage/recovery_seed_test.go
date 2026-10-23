package storage

import (
	"context"
	"os"
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
	eval := func(c PaperDecisionCommand, b PaperDecisionBasis, _ time.Time) (PaperDecisionEvaluation, error) {
		return PaperDecisionEvaluation{Action: "entry_candidate", ReasonCode: "storage_recovery_fixture_only", PriceCents: b.Feed.Routing.Inputs[input.ID].Input.PriceCents, Signal: true, Coverage: domain.MarketCoverageReport{Quote: domain.MarketQuoteCoverage{InputID: input.ID}}}, nil
	}
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
	// Revalidate the other retained owner as well, after every seed write.
	if _, err = s.SocialHistory(ctx); err != nil {
		t.Fatal(err)
	}
}
