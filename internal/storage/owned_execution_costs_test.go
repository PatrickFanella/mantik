package storage

import (
	"context"
	"testing"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/allocation"
	"git.subcult.tv/subculture-collective/mantik/internal/paperfill"
	"git.subcult.tv/subculture-collective/mantik/internal/paperstrategy"
)

func TestExecutionCostBoundsPartialFillFees(t *testing.T) {
	at := time.Date(2026, 10, 22, 14, 0, 0, 0, time.UTC)
	m := paperfill.Model{Method: paperfill.Method, SlippageBPS: 100, FeeBPS: 10, CommissionPerFillCents: 7, LiquidityBPS: 10000, MaxQuoteAgeSeconds: 60}
	d := strategyDefinition("cost-baseline")
	d.Quantity = 3
	d.EntryAtOrBelowCents = 10000
	d.Safety.MaxNotionalCents = 30000
	strategy := paperstrategy.Record{Command: paperstrategy.Command{Definition: d}}
	decision := PaperDecisionRecord{Command: PaperDecisionCommand{ID: "decision"}, Evaluation: PaperDecisionEvaluation{PriceCents: 9000}}
	policy := allocation.PolicyRecord{Command: allocation.PolicyCommand{ExecutionModel: &m}}
	cost, err := executionCost(policy, decision, strategy, at.Add(time.Minute))
	if err != nil || cost.LimitCents != 9090 || cost.GrossCeilingCents != 27270 || cost.FeeCeilingCents != 51 || cost.MaxDebitCents != 27321 {
		t.Fatal(cost, err)
	}
	order := paperfill.Order{ID: "order", Symbol: "SPY", Currency: "USD", Side: "buy", Kind: "limit", Quantity: 3, LimitCents: cost.LimitCents, MaxDebitCents: cost.MaxDebitCents, PlacedAt: at, ExpiresAt: cost.ValidUntil, Mode: "fixture"}
	journal, err := paperfill.New(m, order)
	if err != nil {
		t.Fatal(err)
	}
	var projection paperfill.Projection
	for i, id := range []string{"one", "two", "three"} {
		tick := at.Add(time.Duration(i+1) * time.Second)
		quote := paperfill.Quote{ID: id, SourceSHA256: cost.SHA256, Symbol: "SPY", Currency: "USD", Quality: "usable", EvidenceClass: "synthetic_unqualified", OccurredAt: tick, ReceivedAt: tick, BidCents: 8990, AskCents: 9000, BidQuantity: 1, AskQuantity: 1}
		journal, projection, _, err = paperfill.Apply(context.Background(), journal, paperfill.Command{ID: id, At: tick, Quote: &quote})
		if err != nil {
			t.Fatal(err)
		}
	}
	if projection.Status != "filled" || projection.DebitCents != cost.MaxDebitCents || projection.FeeCents != cost.FeeCeilingCents {
		t.Fatal(projection, cost)
	}
	// Strategy entry caps limit the budget even when modeled slippage is larger.
	decision.Evaluation.PriceCents = 10000
	capped, err := executionCost(policy, decision, strategy, cost.ValidUntil)
	if err != nil || capped.LimitCents != 10000 || capped.GrossCeilingCents != 30000 {
		t.Fatal(capped, err)
	}
	policy.Command.ExecutionModel = nil
	if _, err = executionCost(policy, decision, strategy, cost.ValidUntil); err == nil {
		t.Fatal("missing model")
	}
	policy.Command.ExecutionModel = &m
	m.Method = "unknown"
	if _, err = executionCost(policy, decision, strategy, cost.ValidUntil); err == nil {
		t.Fatal("unknown method")
	}
	m.Method = paperfill.Method
	d.Quantity = paperfill.MaxQuantity + 1
	strategy.Command.Definition = d
	if _, err = executionCost(policy, decision, strategy, cost.ValidUntil); err == nil {
		t.Fatal("quantity outside model")
	}
}
