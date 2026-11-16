package service

import (
	"context"
	"encoding/json"
	"reflect"
	"slices"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/account"
	"git.subcult.tv/subculture-collective/mantik/internal/allocation"
	"git.subcult.tv/subculture-collective/mantik/internal/executioncost"
	"git.subcult.tv/subculture-collective/mantik/internal/forward"
	"git.subcult.tv/subculture-collective/mantik/internal/marketdata"
	"git.subcult.tv/subculture-collective/mantik/internal/strategy"
	"git.subcult.tv/subculture-collective/mantik/internal/strategygate"
	"git.subcult.tv/subculture-collective/mantik/internal/workerplan"
)

// retainQualificationViews takes source evidence from compiled service views,
// never a caller's supplied decision/result. Original bar clocks remain intact.
func retainQualificationViews(p *account.IncubationPlan, views []strategy.View) error {
	if p.Qualification == nil {
		return nil
	}
	q := *p.Qualification
	q.DecisionSources = nil
	for i, v := range views {
		if v.At().IsZero() {
			raw, e := json.Marshal(struct {
				At          time.Time
				CodeControl account.IncubationDecision
			}{p.DecidedAt, p.Decisions[i]})
			if e != nil {
				return e
			}
			q.DecisionSources = append(q.DecisionSources, raw)
			continue
		}
		bars := map[string][]marketdata.AdjustedBar{}
		for _, asset := range v.Assets() {
			bars[asset] = v.Bars(asset)
		}
		raw, e := json.Marshal(struct {
			Universe       []string
			UniverseReport *marketdata.UniverseReport
			At             time.Time
			Assets         []string
			Bars           map[string][]marketdata.AdjustedBar
			Positions      []strategy.Position
			Fills          []strategy.Fill
			State          []byte
		}{v.Universe(), v.UniverseReport(), v.At(), v.Assets(), bars, v.Positions(), v.Fills(), v.State()})
		if e != nil {
			return e
		}
		q.DecisionSources = append(q.DecisionSources, raw)
	}
	p.Qualification = &q
	return nil
}

func (r IncubationRunner) validateQualificationRequest(ctx context.Context, req IncubationRequest) error {
	if req.Qualification == nil {
		return nil
	}
	if req.Protective || req.Qualification.SearchTotal != nil || len(req.Qualification.DecisionSources) != 0 || len(req.Qualification.Input) != 0 || len(req.QualificationInput) == 0 {
		return forward.ErrInvalid
	}
	s, ok := r.Store.(forward.AssessmentStore)
	if !ok {
		return forward.ErrInvalid
	}
	p, e := s.ForwardAssessmentPlan(ctx, req.Qualification.PlanID)
	if e != nil {
		return e
	}
	fs, ok := r.Store.(forward.Store)
	if !ok {
		return forward.ErrInvalid
	}
	w, e := fs.ForwardDeclaration(ctx, p.Plan.DeclarationID)
	if e != nil {
		return e
	}
	d := w.Declaration
	if len(req.Strategies) != 1 || req.Strategies[0].SleeveID != d.SleeveID || req.Strategies[0].GateID != d.GateID || req.DecidedAt.Before(d.WindowStart) || !req.ExecuteAt.Before(d.OutcomeDeadline) || req.Qualification.Benchmark.Validate(req.ExecuteAt) != nil || req.Qualification.Benchmark.StrategyID != d.Benchmark.StrategyID {
		return forward.ErrInvalid
	}
	return nil
}

// EvaluateQualificationCollection is the compiled durable-boundary evaluator.
// It performs no storage or provider reads: the store supplies fenced owners,
// and the manifest supplies original, clock-validated observations. Compiling
// again binds executable behavior to the declaration's immutable strategy ID.
func EvaluateQualificationCollection(ctx context.Context, state account.CappedState, c account.BookCommand, window forward.Record, gate strategygate.Record, total int64) error {
	if c.Kind != "qualification_plan" || c.Incubation == nil || c.Incubation.Qualification == nil {
		return forward.ErrInvalid
	}
	p := c.Incubation
	var input IncubationInput
	if workerplan.ReadBoundedShape(p.Qualification.Input, &input, 32<<20) != nil {
		return forward.ErrInvalid
	}
	req, e := IncubationRequestFromInput(input)
	if e != nil || req.Protective || len(req.Strategies) != 1 || req.Qualification == nil || req.Qualification.PlanID != p.Qualification.PlanID || req.InputSHA256 != p.InputSHA256 || req.ID != c.ID || !req.DecidedAt.Equal(p.DecidedAt) || !req.ExecuteAt.Equal(c.At) || !reflect.DeepEqual(req.Quotes, p.Quotes) || !reflect.DeepEqual(req.Limits, p.Rebalance.Limits) || !reflect.DeepEqual(req.Fees, p.Rebalance.Fees) || !reflect.DeepEqual(req.Qualification.Benchmark, p.Qualification.Benchmark) {
		return forward.ErrInvalid
	}
	s := req.Strategies[0]
	d := window.Declaration
	if d.BookID != state.Config.ID || p.DecidedAt.Before(d.WindowStart) || !c.At.Before(d.OutcomeDeadline) || p.Qualification.Benchmark.Validate(c.At) != nil || p.Qualification.Benchmark.StrategyID != d.Benchmark.StrategyID || p.Rebalance.ID != "inc-"+c.ID {
		return forward.ErrInvalid
	}

	sid, e := s.Strategy.Spec().ID()
	if e != nil || s.Strategy.Spec().ModelDriven || sid != d.StrategyID || s.SleeveID != d.SleeveID || s.GateID != d.GateID || s.ForwardDeclarationID != d.ID || p.ForwardDeclarations[s.SleeveID] != d.ID || gate.Decision == nil || !sameQualificationAssets(s.Assets, gate.Declaration.Assets) || len(p.Decisions) != 1 || len(p.Rebalance.Targets) != 1 || len(p.Rebalance.Sleeves) != 1 {
		return forward.ErrConflict
	}
	owner := p.Rebalance.Sleeves[0]
	if !gate.Decision.AdmitsIncubation() || gate.Decision.Status != "passed" || gate.Decision.StrategyID != sid || owner.ID != s.SleeveID || owner.StrategyID != sid || owner.GateID != s.GateID || owner.Risk != s.Risk || owner.Stage != "incubation" || owner.ModelDriven || owner.Minimum != gate.Decision.Minimum {
		return forward.ErrConflict
	}
	// Flat exits bypass renewed entry admission, but their modeled costs must
	// still match the immutable gate, both at collection and on retained replay.
	cost, e := executioncost.FromDeclaration(gate.Declaration.BaseCost, false)
	if e != nil || len(p.Costs) != 1 || p.Costs[s.SleeveID] != cost {
		return forward.ErrConflict
	}
	book, e := incubationStrategyBook(state, s.SleeveID, sid, p.DecidedAt)
	if e != nil {
		return e
	}
	opened, e := account.ForwardHoldingOpened(state, s.SleeveID, p.DecidedAt)
	if e != nil {
		return e
	}
	a, b := window.Clocks(p.DecidedAt, total, opened), window.Clocks(c.At, total, opened)
	var decision strategy.Decision
	var view strategy.View
	hold := false
	controlled := !a.EntryEligible || !b.EntryEligible || !opened.IsZero() && opened.Before(d.WindowStart)
	if controlled {
		decision.State = book.State
		hold = a.OutcomeOpen && b.OutcomeOpen && !a.HoldExpired && !b.HoldExpired && !opened.IsZero() && !opened.Before(d.WindowStart)
	}
	if account.ReviewExitRequired(state, s.SleeveID, sid, p.DecidedAt) {
		controlled = true
		hold = false
		decision = strategy.Decision{State: book.State}
	}
	if !controlled {
		view, e = strategy.Build(ctx, s.Strategy.Spec(), s.Source, p.DecidedAt, s.Assets, book)
		if e != nil {
			return e
		}
		decision, e = strategy.Decide(s.Strategy, view)
		if e != nil {
			return e
		}
	}
	target := allocation.TargetDecision{KeepPositions: decision.KeepPositions, HoldExposure: hold}
	for _, v := range decision.Targets {
		target.Targets = append(target.Targets, allocation.WeightTarget{Asset: v.Asset, Weight: v.Weight})
	}
	want := account.IncubationDecision{SleeveID: s.SleeveID, StrategyID: sid, State: decision.State}
	if !reflect.DeepEqual(p.Decisions[0], want) || !reflect.DeepEqual(p.Rebalance.Targets[0], allocation.SleeveTarget{SleeveID: s.SleeveID, Decision: target}) {
		return forward.ErrConflict
	}
	retained := *p
	if e = retainQualificationViews(&retained, []strategy.View{view}); e != nil {
		return e
	}
	if !reflect.DeepEqual(retained.Qualification.DecisionSources, p.Qualification.DecisionSources) {
		return forward.ErrConflict
	}
	return nil
}
func sameQualificationAssets(a, b []string) bool {
	a = slices.Clone(a)
	b = slices.Clone(b)
	slices.Sort(a)
	slices.Sort(b)
	return slices.Equal(a, b)
}
