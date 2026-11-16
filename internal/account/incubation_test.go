package account

import (
	"math/big"
	"strings"
	"testing"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/allocation"
	"git.subcult.tv/subculture-collective/mantik/internal/executioncost"
)

func TestIncubationModelChoosesWorseTotalCashEffect(t *testing.T) {
	for _, tc := range []struct{ name, side, venue, fee, want string }{
		{"buy-model-worse", "buy", "100", "0", "-100.050000000000"},
		{"buy-venue-worse", "buy", "101", "0", "-101.000000000000"},
		{"buy-venue-fee-worse", "buy", "100", "1", "-101.000000000000"},
		{"sell-model-worse", "sell", "100", "0", "99.950000000000"},
		{"sell-venue-worse", "sell", "99", "0", "99.000000000000"},
		{"sell-venue-fee-worse", "sell", "100", "1", "99.000000000000"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			at := time.Now().UTC()
			limit := "110"
			if tc.side == "sell" {
				limit = "90"
			}
			o := BookOrder{ID: "owned", AllocationID: "allocation", SleeveID: "sleeve", Symbol: "SPY", Quantity: "1", Limit: limit, Side: tc.side, At: at}
			plan := IncubationPlan{Rebalance: allocation.RebalanceRequest{ID: "allocation"}, Costs: map[string]executioncost.Model{"sleeve": {Version: "same-as-backtest-v1", SlippageBps: "5", FeeBps: "0"}}, Quotes: map[string]IncubationQuote{"SPY": {Symbol: "SPY", Bid: "100", Ask: "100", BidQuantity: "1", AskQuantity: "1", OccurredAt: at, ReceivedAt: at, SourceSHA256: strings.Repeat("a", 64)}}}
			state := CappedState{Events: []BookCommand{{Kind: "incubation_plan", Incubation: &plan}, {Kind: "portfolio_reserve", Order: &o}}}
			f := BookFill{OrderID: o.ID, Symbol: o.Symbol, Side: tc.side, Quantity: "1", Price: tc.venue, Fee: tc.fee, At: at.Add(time.Second)}
			got, e := ModelIncubationFill(state, f)
			if e != nil || got.CashEffect != tc.want || got.QualificationCredit {
				t.Fatal(got, e)
			}
			// Partial quantities retain the same price and do not consume the displayed
			// reservation twice. Fee costs remain per actual venue activity.
			f.Quantity = "0.5"
			f.Fee = "0"
			half, e := ModelIncubationFill(state, f)
			if e != nil || half.ModelPrice != got.ModelPrice {
				t.Fatal(half, e)
			}
			f.At = at.Add(-time.Second)
			if _, e = ModelIncubationFill(state, f); e == nil {
				t.Fatal("fill before submission")
			}
			f.At = at.Add(25 * time.Hour)
			if _, e = ModelIncubationFill(state, f); e == nil {
				t.Fatal("fill outside day order")
			}
		})
	}
}
func TestIncubationQuoteClocksAndClass(t *testing.T) {
	execution := time.Now().UTC()
	decision := execution.Add(-time.Hour)
	q := IncubationQuote{Symbol: "SPY", Bid: "100", Ask: "101", BidQuantity: "1", AskQuantity: "1", OccurredAt: execution.Add(-time.Second), ReceivedAt: execution, SourceSHA256: strings.Repeat("a", 64), EvidenceClass: "prospective_market_unqualified"}
	for _, tc := range []struct {
		name   string
		change func(*IncubationQuote)
	}{
		{"future-market", func(q *IncubationQuote) { q.OccurredAt = execution.Add(time.Second) }},
		{"future-receipt", func(q *IncubationQuote) { q.ReceivedAt = execution.Add(time.Second) }},
		{"before-decision", func(q *IncubationQuote) { q.OccurredAt = decision }},
		{"stale", func(q *IncubationQuote) { q.OccurredAt = execution.Add(-61 * time.Second) }},
		{"synthetic", func(q *IncubationQuote) { q.EvidenceClass = "synthetic_unqualified" }},
		{"unclassified", func(q *IncubationQuote) { q.EvidenceClass = "" }},
		{"crossed", func(q *IncubationQuote) { q.Ask = "99" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			changed := q
			tc.change(&changed)
			if e := ValidateIncubationQuote(changed, decision, execution); e == nil {
				t.Fatal("invalid observation accepted")
			}
		})
	}
	if e := ValidateIncubationQuote(q, decision, execution); e != nil {
		t.Fatal(e)
	}
}
func TestIncubationGateCostFeeIsIncluded(t *testing.T) {
	at := time.Now().UTC()
	o := BookOrder{ID: "order", AllocationID: "a", SleeveID: "s", Symbol: "SPY", Quantity: "0.5", Limit: "101", Side: "buy", At: at}
	p := IncubationPlan{Rebalance: allocation.RebalanceRequest{ID: "a"}, Quotes: map[string]IncubationQuote{"SPY": {Ask: "100", AskQuantity: "1", SourceSHA256: strings.Repeat("a", 64)}}, Costs: map[string]executioncost.Model{"s": {Version: "fee-v1", FeeBps: "10", SlippageBps: "0"}}}
	state := CappedState{Events: []BookCommand{{Kind: "incubation_plan", Incubation: &p}, {Kind: "portfolio_reserve", Order: &o}}}
	f := BookFill{OrderID: o.ID, Symbol: o.Symbol, Side: o.Side, Quantity: o.Quantity, Price: "100", Fee: "0", At: at}
	model, e := ModelIncubationFill(state, f)
	if e != nil || model.ModelFee != "0.050000000000" || model.CashEffect != "-50.050000000000" {
		t.Fatal(model, e)
	}
	v, _ := new(big.Rat).SetString(model.CostPenalty)
	if v.Cmp(big.NewRat(5, 100)) != 0 {
		t.Fatal("cost penalty dropped fee", model)
	}
}

func TestIncubationPayloadsRequireTheirEventKinds(t *testing.T) {
	at := time.Date(2026, 11, 16, 12, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name    string
		command BookCommand
	}{
		{"plan-on-observation", BookCommand{Incubation: &IncubationPlan{}}},
		{"model-on-observation", BookCommand{ModelFill: &IncubationFill{CostPenalty: "-1000"}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, e := NewCappedState(DefaultCappedConfig("book", "paper", Cash, at))
			if e != nil {
				t.Fatal(e)
			}
			c := tc.command
			c.ID, c.Kind, c.At = "observation", "risk_observe", at
			if _, e = ApplyCappedCommand(s, c); e == nil {
				t.Fatal("unrelated command accepted incubation payload")
			}
			// Loaded history must fail closed too, before producing any return.
			s.Events, s.Version = []BookCommand{c}, 1
			if _, e = ApplyCappedCommand(s, c); e == nil {
				t.Fatal("idempotent retry accepted malformed payload")
			}
			if _, e = IncubationReturns(s, nil, at); e == nil {
				t.Fatal("malformed retained history produced an incubation return")
			}
		})
	}
}

func TestIncubationModelIgnoresPlanOnUnrelatedEvent(t *testing.T) {
	at := time.Now().UTC()
	p := IncubationPlan{Rebalance: allocation.RebalanceRequest{ID: "allocation"}}
	o := BookOrder{ID: "owned", AllocationID: "allocation"}
	s := CappedState{Events: []BookCommand{{Kind: "risk_observe", Incubation: &p}, {Kind: "portfolio_reserve", Order: &o}}}
	if model, e := ModelIncubationFill(s, BookFill{OrderID: o.ID, At: at}); e != nil || model != nil {
		t.Fatal("unrelated event supplied a model plan", model, e)
	}
}
