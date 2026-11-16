package account

import (
	"encoding/json"
	"errors"
	"math/big"
	"reflect"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/allocation"
	"git.subcult.tv/subculture-collective/mantik/internal/executioncost"
	"git.subcult.tv/subculture-collective/mantik/internal/forward"
	"git.subcult.tv/subculture-collective/mantik/internal/workhealth"
)

// IncubationQuote is a retained USD execution observation. SourceSHA256 binds
// the original provider response, which stays outside the book and Git.
type IncubationQuote struct {
	Symbol, Bid, Ask, BidQuantity, AskQuantity, SourceSHA256, EvidenceClass string
	OccurredAt, ReceivedAt                                                  time.Time
}

// IncubationDecision retains the strategy's next state with its complete targets
// in Rebalance. It grants no execution or qualification authority.
type IncubationDecision struct {
	SleeveID, StrategyID string
	State                []byte
}

// IncubationPlan is recorded before sizing or submitting. Quotes model immediate
// execution at Rebalance.At, independently of the venue's later fill clock.
type IncubationPlan struct {
	Qualification *forward.Collection `json:",omitempty"`
	// Health is retained observation evidence, never renewed submission authority.
	Health *workhealth.Report `json:",omitempty"`
	// ForwardDeclarations binds optional prospective evidence clocks by sleeve.
	ForwardDeclarations map[string]string `json:",omitempty"`
	Costs               map[string]executioncost.Model
	InputSHA256         string
	DecidedAt           time.Time
	Rebalance           allocation.RebalanceRequest
	Quotes              map[string]IncubationQuote
	Decisions           []IncubationDecision
}

// ValidateIncubationQuote enforces the fixed 60-second execution freshness bound.
// Recorded and synthetic observations cannot masquerade as prospective quotes.
func ValidateIncubationQuote(q IncubationQuote, decision, execution time.Time) error {
	bid, be := bookRat(q.Bid)
	ask, ae := bookRat(q.Ask)
	_, bqe := bookRat(q.BidQuantity)
	_, aqe := bookRat(q.AskQuantity)
	if !bookSymbol.MatchString(q.Symbol) || !hashText.MatchString(q.SourceSHA256) || q.EvidenceClass != "prospective_market_unqualified" || be != nil || ae != nil || bqe != nil || aqe != nil || bid.Sign() <= 0 || ask.Cmp(bid) < 0 || q.OccurredAt.IsZero() || !q.OccurredAt.After(decision) || q.ReceivedAt.Before(q.OccurredAt) || q.ReceivedAt.After(execution) || execution.Sub(q.OccurredAt) > time.Minute {
		return errors.New("incubation requires a fresh prospective post-decision quote")
	}
	return nil
}

func validateIncubationPlan(s CappedState, c BookCommand) error {
	p := c.Incubation
	if p != nil && ((c.Kind == "qualification_plan") != (p.Qualification != nil)) {
		return errors.New("qualification collection kind differs")
	}
	if p == nil || !incubationPlanJSONSize(*p) || !hashText.MatchString(p.InputSHA256) || p.DecidedAt.IsZero() || !p.Rebalance.At.Equal(c.At) || !p.DecidedAt.Before(c.At) || p.DecidedAt.Before(s.Config.OpenedAt) || len(p.Decisions) != len(p.Rebalance.Sleeves) || len(p.Decisions) == 0 || len(p.Decisions) > 64 || len(p.Quotes) == 0 || len(p.Quotes) > 256 {
		return errors.New("invalid incubation plan")
	}
	// Any preceding application is information unavailable before this decision.
	for _, e := range s.Events {
		if e.ID == c.ID {
			break
		}
		if e.ID == p.Rebalance.ID || e.Order != nil && e.Order.AllocationID == p.Rebalance.ID {
			return errors.New("incubation allocation already exists before plan")
		}
		if IsStrategyPlan(e) && e.Incubation != nil && e.Incubation.Rebalance.ID == p.Rebalance.ID {
			return errors.New("incubation allocation already owned")
		}
		if e.At.After(p.DecidedAt) {
			return errors.New("incubation decision precedes retained book state")
		}
	}
	for symbol, q := range p.Quotes {
		if symbol != q.Symbol {
			return errors.New("quote symbol differs from binding")
		}
		if e := ValidateIncubationQuote(q, p.DecidedAt, c.At); e != nil {
			return e
		}
	}
	seen := map[string]bool{}
	for _, d := range p.Decisions {
		if _, _, e := p.Costs[d.SleeveID].Parse(); e != nil {
			return e
		}
		if seen[d.SleeveID] || len(d.State) > 64<<10 {
			return errors.New("invalid incubation strategy state")
		}
		seen[d.SleeveID] = true
		found := false
		for _, sleeve := range p.Rebalance.Sleeves {
			if sleeve.ID == d.SleeveID && sleeve.StrategyID == d.StrategyID && (sleeve.Stage == "incubation" || sleeve.Stage == "forward_only" || sleeve.Stage == "qualified") {
				found = true
			}
		}
		if !found {
			return errors.New("incubation decision owner differs")
		}
	}
	return nil
}

// IncubationFill keeps the model's result for the venue's actual partial quantity.
// CashEffect is the less favourable total, including fees; CostPenalty is the
// nonnegative difference from venue accounting. The venue ledger stays intact.
type IncubationFill struct {
	Method, QuoteSHA256, ModelPrice, ModelFee, CashEffect, CostPenalty string
	ModeledAt                                                          time.Time
	QualificationCredit                                                bool
}

// ModelIncubationFill computes the gate-declared adverse quote cost model.
// It models only displayed executable liquidity and never assumes an optimistic
// fill when the modeled price crosses the retained day limit.
func ModelIncubationFill(s CappedState, f BookFill) (*IncubationFill, error) {

	plans := map[string]*IncubationPlan{}
	orders := map[string]*BookOrder{}
	for _, event := range s.Events {
		if IsStrategyPlan(event) && event.Incubation != nil {
			plans[event.Incubation.Rebalance.ID] = event.Incubation
		}
		if event.Kind == "portfolio_reserve" && event.Order != nil {
			orders[event.Order.ID] = event.Order
		}
	}
	order := orders[f.OrderID]
	if order == nil {
		return nil, nil
	}
	plan := plans[order.AllocationID]
	if plan == nil {
		return nil, nil
	}
	q, ok := plan.Quotes[f.Symbol]
	if !ok {
		return nil, errors.New("incubation fill has no retained quote")
	}
	if f.At.Before(order.At) || f.At.Sub(order.At) > 24*time.Hour {
		return nil, errors.New("incubation venue fill outside day-order clock")
	}
	qty, qe := bookRat(f.Quantity)
	venue, ve := bookRat(f.Price)
	fee, fe := bookRat(f.Fee)
	if qe != nil || ve != nil || fe != nil || qty.Sign() <= 0 || venue.Sign() <= 0 {
		return nil, errors.New("invalid incubation venue fill")
	}

	reference, displayed := q.Ask, q.AskQuantity
	if f.Side == "sell" {
		reference, displayed = q.Bid, q.BidQuantity
	}
	price, pe := bookRat(reference)
	liquidity, le := bookRat(displayed)
	if pe != nil || le != nil || price.Sign() <= 0 {
		return nil, errors.New("invalid retained model quote")
	}
	rate, slip, costErr := plan.Costs[order.SleeveID].Parse()
	if costErr != nil {
		return nil, costErr
	}
	price = executioncost.Price(price, slip, f.Side == "buy")
	// Reuse of one observation across sleeves or partial fills cannot multiply
	// displayed liquidity. Admission conservatively reserves the full order.
	used := new(big.Rat)

	for _, reserved := range orders {
		if reserved.Symbol != f.Symbol || reserved.Side != f.Side {
			continue
		}
		if p := plans[reserved.AllocationID]; p != nil {
			old := p.Quotes[f.Symbol]
			if old.SourceSHA256 == q.SourceSHA256 || old.OccurredAt.Equal(q.OccurredAt) {
				n, err := bookRat(reserved.Quantity)
				if err != nil {
					return nil, err
				}
				used.Add(used, n)
			}
		}
	}
	limit, limitErr := bookRat(order.Limit)
	if limitErr != nil {
		return nil, limitErr
	}
	if used.Cmp(liquidity) > 0 || f.Side == "buy" && price.Cmp(limit) > 0 || f.Side == "sell" && price.Cmp(limit) < 0 {
		return nil, errors.New("incubation model cannot fill retained order")
	}
	model := new(big.Rat).Mul(qty, price)
	modelFee := new(big.Rat).Mul(model, rate)
	actual := new(big.Rat).Mul(qty, venue)
	if f.Side == "buy" {
		model.Neg(model)
		actual.Neg(actual)
	}
	actual.Sub(actual, fee)
	model.Sub(model, modelFee)
	worse := new(big.Rat).Set(actual)
	if model.Cmp(worse) < 0 {
		worse.Set(model)
	}
	penalty := new(big.Rat).Sub(actual, worse)
	return &IncubationFill{Method: "quote-declared-cost-v1", QuoteSHA256: q.SourceSHA256, ModelPrice: price.FloatString(12), ModelFee: modelFee.FloatString(12), CashEffect: worse.FloatString(12), CostPenalty: penalty.FloatString(12), ModeledAt: order.At}, nil
}

func validateIncubationFill(s CappedState, c BookCommand) error {
	// Consider only the history preceding this fill on replay.
	prefix := s
	for i, e := range s.Events {
		if e.ID == c.ID {
			prefix = CappedPrefix(s, i)
			break
		}
	}
	expected, e := ModelIncubationFill(prefix, *c.Fill)
	if e != nil {
		return e
	}
	if !reflect.DeepEqual(expected, c.ModelFill) {
		return errors.New("retained incubation model fill differs")
	}
	return nil
}

// IncubationPerformance reports a cost-conservative net return on contributed
// capital, not a time-weighted or qualification return. Deposits are not profit.
type IncubationPerformance struct {
	EvidenceClass, Status, CostPenalty, Equity, NetPnL, NetReturnOnContributions string
	QualificationCredit                                                          bool
	Fills                                                                        int
}

// IncubationReturns uses the worse cash effect of every retained fill. It does
// not overwrite venue balances, grant qualification, or publish trial results.
func IncubationReturns(s CappedState, marks map[string]string, at time.Time) (IncubationPerformance, error) {
	out := IncubationPerformance{EvidenceClass: "prospective_market_unqualified", Status: "incubation_unqualified"}
	snap, e := SnapshotCapped(s, marks, at)
	if e != nil {
		return out, e
	}
	penalty := new(big.Rat)
	for _, event := range s.Events {
		if event.Kind == "reserve" || event.Kind == "fill" && event.ModelFill == nil {
			return out, errors.New("book contains execution outside incubation")
		}
	}
	for _, event := range s.Events {
		if event.Kind == "fill" && event.ModelFill != nil {
			v, ok := new(big.Rat).SetString(event.ModelFill.CostPenalty)
			if !ok {
				return out, errors.New("invalid model penalty")
			}
			penalty.Add(penalty, v)
			out.Fills++
		}
	}
	equity, _ := new(big.Rat).SetString(snap.Equity)
	equity.Sub(equity, penalty)
	capital, _ := bookRat(s.Config.OpeningCash)
	flow, _ := bookRat(s.Config.DepositAmount)
	capital.Add(capital, new(big.Rat).Mul(flow, big.NewRat(int64(snap.Deposits), 1)))
	pnl := new(big.Rat).Sub(equity, capital)
	out.CostPenalty = penalty.FloatString(12)
	out.Equity = equity.FloatString(12)
	out.NetPnL = pnl.FloatString(12)
	out.NetReturnOnContributions = new(big.Rat).Quo(pnl, capital).FloatString(12)
	return out, nil
}

// incubationPlanJSONSize bounds a retained strategy/quote plan before storage.
func incubationPlanJSONSize(p IncubationPlan) bool {
	raw, e := json.Marshal(p)
	return e == nil && len(raw) <= 2<<20
}

func checkIncubationOrder(s CappedState, c BookCommand) error {
	for i, e := range s.Events {
		if e.ID == c.ID {
			s = CappedPrefix(s, i+1)
			break
		}
	}
	o := c.Order
	_, err := ModelIncubationFill(s, BookFill{OrderID: o.ID, Symbol: o.Symbol, Side: o.Side, Quantity: o.Quantity, Price: o.Limit, Fee: "0", At: o.At})
	return err
}

func checkIncubationRebalance(s CappedState, c BookCommand) error {
	for _, event := range s.Events {
		if event.ID == c.ID {
			break
		}
		if IsStrategyPlan(event) && event.Incubation != nil && event.Incubation.Rebalance.ID == c.ID {
			if !reflect.DeepEqual(event.Incubation.Rebalance, *c.Rebalance) {
				return errors.New("rebalance differs from retained incubation decisions")
			}
		}
	}
	return nil
}

// IsStrategyPlan recognizes distinct execution modes without retagging history.
func IsStrategyPlan(c BookCommand) bool {
	return c.Kind == "incubation_plan" || c.Kind == "qualification_plan"
}
