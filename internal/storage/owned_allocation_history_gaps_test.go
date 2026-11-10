package storage

import (
	"fmt"
	"reflect"
	"testing"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/domain"
	"git.subcult.tv/subculture-collective/mantik/internal/feedcollector"
	"git.subcult.tv/subculture-collective/mantik/internal/paperstrategy"
)

func TestMemoryAllocationDecisionHistoryGaps(t *testing.T) {
	allocationDecisionHistoryGaps(t, NewMemory())
}

func TestPostgresContractsAllocationDecisionHistoryGaps(t *testing.T) {
	allocationDecisionHistoryGaps(t, timerPostgres(t, "allocationhistorygaps"))
}

// Actual owner records alternate between two distinct strategy source keys.
// The evaluator is a storage fixture, not qualification or model evidence.
func allocationDecisionHistoryGaps(t *testing.T, s ownedAllocationBackend) {
	ownedCheckpointRecords = 3
	defer func() { ownedCheckpointRecords = 64 }()
	ctx := t.Context()
	at := time.Date(2026, 11, 9, 15, 0, 0, 0, time.UTC)
	now := func() time.Time { return at }
	owner := ""
	strategies := []paperstrategy.Record{}
	for i := range 2 {
		account := policyAccount(t, s, fmt.Sprintf("account-%d", i))
		owner = account.ReceiverID
		d := strategyDefinition(fmt.Sprintf("strategy-%d", i))
		d.InputEvidenceClass = "prospective_market_unqualified"
		r, err := s.RegisterPaperStrategy(ctx, paperstrategy.Command{ReceiverID: owner, Definition: d}, now)
		if err != nil {
			t.Fatal(err)
		}
		strategies = append(strategies, r.Record)
		p := policyCommand(account, fmt.Sprintf("policy-%d", i))
		p.Policy.Strategies[0].ID, p.Policy.Strategies[0].ArtifactSHA256 = d.ID, r.Record.DefinitionSHA256
		p.Policy.Symbols[0].ID = "SPY"
		if _, err := s.RegisterAllocationPolicy(ctx, p, now); err != nil {
			t.Fatal(err)
		}
	}
	reg := feedcollector.Registration{ID: "spy-iex", Provider: "alpaca", Feed: "iex", Symbol: "SPY", EvidenceClass: "prospective_market_unqualified", JournalID: "journal-one", AccessRef: "access-one", LeaseSeconds: 300}
	for _, c := range []feedcollector.Command{
		{RequestID: "register", Operation: "register", RegistrationID: reg.ID, Registration: &reg},
		{RequestID: "claim", Operation: "claim", RegistrationID: reg.ID, OwnerID: "worker-one"},
	} {
		if _, err := s.FeedCollectorCommand(ctx, owner, c, now); err != nil {
			t.Fatal(err)
		}
	}
	input := collectorInput(at, "allocation-history-quote")
	if _, err := s.SubmitCollectorResearch(ctx, owner, feedcollector.Fence{RegistrationID: reg.ID, OwnerID: "worker-one", Generation: 1}, input, now); err != nil {
		t.Fatal(err)
	}
	de := func(_ PaperDecisionCommand, b PaperDecisionBasis, _ time.Time) (PaperDecisionEvaluation, error) {
		return PaperDecisionEvaluation{Action: "entry_candidate", ReasonCode: "storage_contract_only", PriceCents: b.Feed.Routing.Inputs[input.ID].Input.PriceCents, Signal: true, Coverage: domain.MarketCoverageReport{Quote: domain.MarketQuoteCoverage{InputID: input.ID}}}, nil
	}
	decisions := []PaperDecisionRecord{}
	for i, strategy := range strategies {
		r, err := s.FinalizePaperDecision(ctx, PaperDecisionCommand{ID: fmt.Sprintf("decision-%d", i), ReceiverID: owner, StrategyID: strategy.Command.Definition.ID, StrategySHA256: strategy.SHA256, RegistrationID: reg.ID}, now, de)
		if err != nil {
			t.Fatal(err)
		}
		decisions = append(decisions, r.Record)
	}
	for i := range 8 {
		d := decisions[i%2]
		if _, err := s.CompareOwnedAllocation(ctx, OwnedAllocationCommand{ID: fmt.Sprintf("comparison-%d", i), ReceiverID: owner, Decisions: []OwnedDecisionPin{{ID: d.Command.ID, SHA256: d.SHA256}}}, func() time.Time { return at.Add(time.Duration(i+1) * time.Second) }, de); err != nil {
			t.Fatal("alternating native comparison", i, err)
		}
	}
	all, err := s.OwnedAllocations(ctx, de)
	if err != nil || len(all.Records) != 8 {
		t.Fatal(err)
	}
	for i, d := range decisions {
		q := OwnedAllocationHistoryQuery{ReceiverID: owner, DecisionID: d.Command.ID, Limit: 1}
		sequences := []int{}
		for {
			p, err := s.(OwnedAllocationHistoryStore).OwnedAllocationHistory(ctx, q, de)
			if err != nil || p.Validate(q) != nil || p.ThroughSequence != 6 {
				t.Fatal("native gapped decision page", err)
			}
			for _, r := range p.Records {
				if r.Comparison == nil || !reflect.DeepEqual(*r.Comparison, all.Records[r.Sequence-1]) {
					t.Fatal("gapped selection changed an original")
				}
				sequences = append(sequences, r.Sequence)
			}
			if p.NextAfter == "" {
				break
			}
			q.After = p.NextAfter
		}
		want := []int{1 + i, 3 + i, 5 + i}
		if !reflect.DeepEqual(sequences, want) {
			t.Fatal("decision history skipped, repeated or included another decision", sequences, want)
		}
	}
	if got, err := s.(OwnedAllocationAuditStore).AuditOwnedAllocations(ctx, de); err != nil || got.Status != "verified" || got.Records != 8 {
		t.Fatal("gapped index replaced native full-history proof", err)
	}
}
