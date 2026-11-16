package service

import (
	"context"
	"errors"
	"fmt"
	"math/big"
	"reflect"
	"regexp"
	"slices"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/account"
	"git.subcult.tv/subculture-collective/mantik/internal/allocation"
	"git.subcult.tv/subculture-collective/mantik/internal/alpaca"
	"git.subcult.tv/subculture-collective/mantik/internal/cappedbook"
	"git.subcult.tv/subculture-collective/mantik/internal/executioncost"
	"git.subcult.tv/subculture-collective/mantik/internal/storage"
	"git.subcult.tv/subculture-collective/mantik/internal/strategy"
	"git.subcult.tv/subculture-collective/mantik/internal/strategygate"
)

var incubationCycleID = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,59}$`)

// IncubationStore adds authoritative gates to the existing book journal.
type IncubationStore interface {
	storage.CappedBookStore
	StrategyGate(context.Context, string) (strategygate.Record, error)
	StrategyTrialSearchTotal(context.Context, string) (int64, error)
}

// IncubatingStrategy binds reviewed code and data to a fixed sleeve. Models can
// supply targets through the same interface; they cannot set stage or budgets.
type IncubatingStrategy struct {
	SleeveID, GateID, Risk string
	Strategy               strategy.Strategy
	Source                 strategy.BarSource
	Assets                 []string
}

// IncubationRequest is one bounded decision/execution cycle. InputSHA256 pins
// the owner's complete input (recipes and retained source manifest included).
// Retrying an ID recovers the recorded decisions without invoking strategies.
type IncubationRequest struct {
	ID, InputSHA256      string
	DecidedAt, ExecuteAt time.Time
	Strategies           []IncubatingStrategy
	Quotes               map[string]account.IncubationQuote
	Limits, Fees         map[string]string
}

// IncubationResult is an inspectable plan, its exact sizing, and paper orders.
// It never creates qualification evidence or changes strategy trial records.
type IncubationResult struct {
	Plan                account.IncubationPlan
	Distribution        allocation.Distribution
	Orders              []alpaca.PaperOrder
	QualificationCredit bool
}

// IncubationRunner runs a cycle against the local capped book, then submits only
// retained sized orders through its existing hard risk and paper adapter path.
type IncubationRunner struct {
	Store  IncubationStore
	Broker cappedbook.Broker
	BookID string
	now    func() time.Time
}

func incubationMarks(quotes map[string]account.IncubationQuote) map[string]string {
	out := map[string]string{}
	for symbol, q := range quotes {
		b, bok := new(big.Rat).SetString(q.Bid)
		a, aok := new(big.Rat).SetString(q.Ask)
		if bok && aok {
			out[symbol] = new(big.Rat).Quo(new(big.Rat).Add(b, a), big.NewRat(2, 1)).FloatString(9)
		}
	}
	return out
}

// Run records decisions before sizing and submission. An already reserved ID is
// recovered by GET only; a crash or ambiguous POST can never cause another POST.
// A later invocation needs a fresh cycle ID and quote clock.
func (r IncubationRunner) Run(ctx context.Context, req IncubationRequest) (IncubationResult, error) {
	out := IncubationResult{Orders: []alpaca.PaperOrder{}}
	if !incubationCycleID.MatchString(req.ID) {
		return out, errors.New("invalid incubation cycle ID")
	}
	if r.Store == nil || r.Broker == nil {
		return out, errors.New("incubation requires store and paper broker")
	}
	state, e := r.Store.CappedBook(ctx, r.BookID)
	if e != nil {
		return out, e
	}
	if state.Config.BrokerAccountID != r.Broker.PaperAccountID() {
		return out, errors.New("paper account differs from incubation book")
	}
	var plan *account.IncubationPlan
	for _, event := range state.Events {
		if event.ID == req.ID {
			if event.Kind != "incubation_plan" || event.Incubation == nil || event.Incubation.InputSHA256 != req.InputSHA256 || !event.Incubation.DecidedAt.Equal(req.DecidedAt) || !event.At.Equal(req.ExecuteAt) || !reflect.DeepEqual(event.Incubation.Quotes, req.Quotes) || !reflect.DeepEqual(event.Incubation.Rebalance.Limits, req.Limits) || !reflect.DeepEqual(event.Incubation.Rebalance.Fees, req.Fees) {
				return out, account.ErrBookConflict
			}
			plan = event.Incubation
		}
	}
	client := cappedbook.Client{Store: r.Store, Broker: r.Broker, BookID: r.BookID}
	if plan == nil {
		now := time.Now().UTC()
		if r.now != nil {
			now = r.now()
		}
		if now.Before(req.ExecuteAt) || now.Sub(req.ExecuteAt) > time.Minute {
			return out, errors.New("incubation execution clock expired")
		}
		if len(req.Strategies) == 0 || len(req.Strategies) > 64 || !req.DecidedAt.Before(req.ExecuteAt) {
			return out, errors.New("incubation requires bounded strategies and ordered clocks")
		}
		if len(state.Events) > 0 && state.Events[len(state.Events)-1].At.After(req.DecidedAt) {
			return out, errors.New("decision precedes retained book state")
		}
		for _, q := range req.Quotes {
			if e = account.ValidateIncubationQuote(q, req.DecidedAt, req.ExecuteAt); e != nil {
				return out, e
			}
		}
		p := account.IncubationPlan{Costs: map[string]executioncost.Model{}, InputSHA256: req.InputSHA256, DecidedAt: req.DecidedAt, Quotes: req.Quotes, Rebalance: allocation.RebalanceRequest{ID: "inc-" + req.ID, At: req.ExecuteAt, Limits: req.Limits, Fees: req.Fees}}
		roster, err := account.PortfolioRoster(state)
		if err != nil {
			return out, err
		}
		// Check new admissions before invoking strategies. A stale retained owner
		// may decide only an explicit flat exit; the store repeats this exception
		// and freshness atomically when retaining the plan and orders.
		gates := make([]strategygate.Record, len(req.Strategies))
		stale := make([]bool, len(req.Strategies))
		seen := map[string]bool{}
		versions := map[string]bool{}
		for i, s := range req.Strategies {
			if s.Strategy == nil || seen[s.SleeveID] {
				return out, errors.New("distinct compiled incubation strategies required")
			}
			seen[s.SleeveID] = true
			sid, err := s.Strategy.Spec().ID()
			if err != nil {
				return out, err
			}
			if versions[sid] {
				return out, errors.New("strategy version already owns an incubation sleeve")
			}
			versions[sid] = true
			g, err := r.Store.StrategyGate(ctx, s.GateID)
			if err != nil {
				return out, err
			}
			if g.Decision == nil || !g.Decision.AdmitsIncubation() || g.Decision.StrategyID != sid || g.Decision.At.After(req.DecidedAt) || g.DeclaredAt.After(req.DecidedAt) || g.Declaration.Account != string(state.Config.Type) || s.Strategy.Spec().ModelDriven != (g.Decision.Status == "forward_only") {
				return out, errors.New("eligible incubation gate required before decision")
			}
			assets := slices.Clone(s.Assets)
			declaredAssets := slices.Clone(g.Declaration.Assets)
			slices.Sort(assets)
			slices.Sort(declaredAssets)
			if (!s.Strategy.Spec().ModelDriven || len(declaredAssets) > 0) && !slices.Equal(assets, declaredAssets) {
				return out, errors.New("incubation asset set differs from gate")
			}
			n, err := r.Store.StrategyTrialSearchTotal(ctx, g.Declaration.Family)
			if err != nil {
				return out, err
			}
			if n != g.Decision.SearchTotal {
				owner, retained := roster[s.SleeveID]
				if !retained || owner.StrategyID != sid || owner.GateID != s.GateID {
					return out, errors.New("gate_search_history_changed")
				}
				stale[i] = true
			}
			gates[i] = g
		}
		for i, s := range req.Strategies {
			g := gates[i]
			cost, costErr := executioncost.FromDeclaration(g.Declaration.BaseCost, s.Strategy.Spec().ModelDriven)
			if costErr != nil {
				return out, costErr
			}
			p.Costs[s.SleeveID] = cost
			stage := "incubation"
			if g.Decision.Status == "forward_only" {
				stage = "forward_only"
			}
			book, err := incubationStrategyBook(state, s.SleeveID, g.Decision.StrategyID, req.DecidedAt)
			if err != nil {
				return out, err
			}
			view, err := strategy.Build(ctx, s.Strategy.Spec(), s.Source, req.DecidedAt, s.Assets, book)
			if err != nil {
				return out, err
			}
			d, err := strategy.Decide(s.Strategy, view)
			if err != nil {
				return out, err
			}
			target := allocation.TargetDecision{KeepPositions: d.KeepPositions}
			for _, t := range d.Targets {
				if _, ok := req.Quotes[t.Asset]; !ok {
					return out, errors.New("target missing execution quote")
				}
				target.Targets = append(target.Targets, allocation.WeightTarget{Asset: t.Asset, Weight: t.Weight})
			}
			flat := allocation.FlatTarget(target)
			if stale[i] && !flat {
				return out, errors.New("gate_search_history_changed")
			}
			sleeve := allocation.Sleeve{ID: s.SleeveID, StrategyID: g.Decision.StrategyID, Stage: stage, ModelDriven: stage == "forward_only", GateID: s.GateID, Risk: s.Risk, Minimum: g.Decision.Minimum}
			if owner, retained := roster[s.SleeveID]; retained && flat && owner.StrategyID == sleeve.StrategyID && owner.GateID == sleeve.GateID {
				sleeve = owner
			}
			p.Rebalance.Sleeves = append(p.Rebalance.Sleeves, sleeve)
			p.Rebalance.Targets = append(p.Rebalance.Targets, allocation.SleeveTarget{SleeveID: s.SleeveID, Decision: target})
			p.Decisions = append(p.Decisions, account.IncubationDecision{SleeveID: s.SleeveID, StrategyID: g.Decision.StrategyID, State: d.State})
		}
		// Due flows precede the plan; the decision must not see execution-time flows.
		// Fund's receipt clock is execution time, so record the plan first.
		state, e = r.Store.ApplyCappedBook(ctx, r.BookID, state.Version, account.BookCommand{ID: req.ID, Kind: "incubation_plan", At: req.ExecuteAt, Incubation: &p})
		if e != nil {
			return out, e
		}
		plan = state.Events[len(state.Events)-1].Incubation
	}
	client.Broker = incubationSubmissionBroker{Broker: r.Broker, plan: *plan, now: r.now}
	out.Plan = *plan
	marks := incubationMarks(plan.Quotes)
	dist, e := client.Rebalance(ctx, plan.Rebalance, marks)
	if e != nil {
		return out, e
	}
	out.Distribution = dist
	for _, o := range dist.Orders {
		if e = ctx.Err(); e != nil {
			return out, e
		}
		state, e = r.Store.CappedBook(ctx, r.BookID)
		if e != nil {
			return out, e
		}
		reserved := false
		rejected := false
		for _, event := range state.Events {
			if event.Kind == "portfolio_reserve" && event.Order.ID == o.ID {
				reserved = true
			}
			if event.Kind == "reject" && event.OrderID == o.ID {
				rejected = true
			}
		}
		if rejected {
			continue
		}
		if reserved {
			// Sync handles both a missing adapter intent and terminal fills, without
			// ever submitting. Recovery may happen long after the original quote.
			v, err := r.Broker.Order(ctx, o.ID)
			if err != nil {
				return out, fmt.Errorf("incubation recovery requires sync: %w", err)
			}
			out.Orders = append(out.Orders, v)
			continue
		}
		// Never POST previously unsent work from an obsolete distribution.
		// A restart after any later receipt is reconciliation-only.
		if len(state.Events) > 0 && state.Events[len(state.Events)-1].At.After(plan.Rebalance.At) {
			return out, errors.New("incubation unsent order expired; start a fresh cycle")
		}
		now := time.Now().UTC()
		if r.now != nil {
			now = r.now()
		}
		if now.Before(plan.Rebalance.At) || now.Sub(plan.Rebalance.At) > time.Minute {
			return out, errors.New("incubation unsent order expired; start a fresh cycle")
		}
		for _, q := range plan.Quotes {
			if err := account.ValidateIncubationQuote(q, plan.DecidedAt, now); err != nil {
				return out, err
			}
		}
		order := account.BookOrder{ID: o.ID, Symbol: o.Symbol, Quantity: o.Quantity, Limit: o.Limit, Side: o.Side, Fee: o.Fee, At: o.At, AllocationID: o.AllocationID, SleeveID: o.SleeveID, StrategyID: o.StrategyID, ReduceOnly: o.ReduceOnly}
		v, err := client.Submit(ctx, order, marks)
		if err != nil {
			return out, err
		}
		out.Orders = append(out.Orders, v)
	}
	return out, nil
}

// Sync reconciles owned venue fills, retaining their model comparisons, then
// reports only cost-conservative incubation returns with no qualification credit.
func (r IncubationRunner) Sync(ctx context.Context, at time.Time, quotes map[string]account.IncubationQuote) (account.IncubationPerformance, error) {
	marks := incubationMarks(quotes)
	for symbol, q := range quotes {
		if symbol != q.Symbol {
			return account.IncubationPerformance{}, errors.New("quote binding differs")
		}
		if e := account.ValidateIncubationQuote(q, at.Add(-time.Minute-time.Nanosecond), at); e != nil {
			return account.IncubationPerformance{}, e
		}
	}
	c := cappedbook.Client{Store: r.Store, Broker: r.Broker, BookID: r.BookID}
	if _, e := c.Sync(ctx, at, marks); e != nil {
		return account.IncubationPerformance{}, e
	}
	state, e := r.Store.CappedBook(ctx, r.BookID)
	if e != nil {
		return account.IncubationPerformance{}, e
	}
	return account.IncubationReturns(state, marks, at)
}

func incubationStrategyBook(s account.CappedState, sleeve, sid string, at time.Time) (strategy.Book, error) {
	out := strategy.Book{}
	orders := map[string]account.BookOrder{}
	positions := map[string]*big.Rat{}
	for _, e := range s.Events {
		if e.At.After(at) {
			return out, errors.New("strategy book contains future receipt")
		}
		if e.Kind == "incubation_plan" && e.Incubation != nil {
			for _, d := range e.Incubation.Decisions {
				if d.SleeveID == sleeve {
					if d.StrategyID != sid {
						return out, errors.New("sleeve owned by another strategy")
					}
					out.State = slices.Clone(d.State)
				}
			}
		}
		if e.Kind == "portfolio_reserve" && e.Order.SleeveID == sleeve {
			if e.Order.StrategyID != sid {
				return out, errors.New("sleeve order owned by another strategy")
			}
			orders[e.Order.ID] = *e.Order
		}
		if e.Kind == "fill" {
			if _, owned := orders[e.Fill.OrderID]; owned {
				f := e.Fill
				if f.At.After(at) {
					return out, strategy.ErrFutureData
				}
				q, _ := new(big.Rat).SetString(f.Quantity)
				if f.Side == "sell" {
					q.Neg(q)
				}
				if positions[f.Symbol] == nil {
					positions[f.Symbol] = new(big.Rat)
				}
				positions[f.Symbol].Add(positions[f.Symbol], q)
				price := f.Price
				if e.ModelFill != nil {
					model, _ := new(big.Rat).SetString(e.ModelFill.ModelPrice)
					venue, _ := new(big.Rat).SetString(price)
					if f.Side == "buy" && model.Cmp(venue) > 0 || f.Side == "sell" && model.Cmp(venue) < 0 {
						price = e.ModelFill.ModelPrice
					}
				}
				out.Fills = append(out.Fills, strategy.Fill{Asset: f.Symbol, Time: f.At, Quantity: q.FloatString(9), Price: price})
			}
		}
	}
	symbols := []string{}
	for symbol := range positions {
		symbols = append(symbols, symbol)
	}
	slices.Sort(symbols)
	for _, symbol := range symbols {
		if positions[symbol].Sign() != 0 {
			out.Positions = append(out.Positions, strategy.Position{Asset: symbol, Quantity: positions[symbol].FloatString(9)})
		}
	}
	return out, nil
}

// IncubationInputHash pins a complete CLI input, without retaining provider
// secrets or a second copy of the source data in the capped book.
func IncubationInputHash(v any) (string, error) { return strategygate.Hash(v) }

// Recheck the real clock at the final adapter boundary, after ledger replay and
// reservation work. A slow store cannot make an expired quote executable.
type incubationSubmissionBroker struct {
	cappedbook.Broker
	plan account.IncubationPlan
	now  func() time.Time
}

func (b incubationSubmissionBroker) Submit(ctx context.Context, o alpaca.StockOrder) (alpaca.PaperOrder, error) {
	check := func() error { return b.checkSubmission(o) }
	if broker, ok := b.Broker.(interface {
		SubmitChecked(context.Context, alpaca.StockOrder, func() error) (alpaca.PaperOrder, error)
	}); ok {
		return broker.SubmitChecked(ctx, o, check)
	}
	if err := check(); err != nil {
		return alpaca.PaperOrder{}, err
	}
	return b.Broker.Submit(ctx, o)
}

func (b incubationSubmissionBroker) checkSubmission(o alpaca.StockOrder) error {
	at := time.Now().UTC()
	if b.now != nil {
		at = b.now()
	}
	if at.Before(b.plan.Rebalance.At) || at.Sub(b.plan.Rebalance.At) > time.Minute {
		return errors.New("incubation quote expired before submission")
	}
	q, ok := b.plan.Quotes[o.Symbol]
	if !ok {
		return errors.New("incubation quote missing before submission")
	}
	if e := account.ValidateIncubationQuote(q, b.plan.DecidedAt, at); e != nil {
		return e
	}
	return nil
}
