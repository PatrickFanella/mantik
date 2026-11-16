package storage

import (
	"context"
	"errors"
	"slices"

	"git.subcult.tv/subculture-collective/mantik/internal/account"
	"git.subcult.tv/subculture-collective/mantik/internal/allocation"
	"git.subcult.tv/subculture-collective/mantik/internal/executioncost"
	"git.subcult.tv/subculture-collective/mantik/internal/forward"
	"git.subcult.tv/subculture-collective/mantik/internal/strategygate"
	"git.subcult.tv/subculture-collective/mantik/internal/strategytrial"
	"github.com/jackc/pgx/v5"
)

func admissionRequest(s account.CappedState, c account.BookCommand) (*allocation.RebalanceRequest, error) {
	if account.IsStrategyPlan(c) {
		if c.Incubation == nil {
			return nil, errors.New("missing incubation plan")
		}
		return &c.Incubation.Rebalance, nil
	}
	if c.Kind == "rebalance" {
		if c.Rebalance == nil {
			return nil, errors.New("missing distributor request")
		}
		return c.Rebalance, nil
	}
	if c.Kind != "portfolio_reserve" {
		return nil, nil
	}
	if c.Order == nil {
		return nil, errors.New("missing portfolio order")
	}
	for i := len(s.Events) - 1; i >= 0; i-- {
		v := s.Events[i]
		if v.Kind == "rebalance" && v.ID == c.Order.AllocationID {
			return v.Rebalance, nil
		}
	}
	return nil, errors.New("distributor_order_required")
}

func neededAdmissions(s account.CappedState, c account.BookCommand) ([]allocation.Sleeve, error) {
	req, e := admissionRequest(s, c)
	if e != nil || req == nil {
		return nil, e
	}
	roster, e := account.PortfolioRoster(s)
	if e != nil {
		return nil, e
	}
	flat := map[string]bool{}
	for _, t := range req.Targets {
		flat[t.SleeveID] = allocation.FlatTarget(t.Decision)
	}
	needed := []allocation.Sleeve{}
	for _, v := range req.Sleeves {
		if c.Kind == "portfolio_reserve" && c.Order.SleeveID != v.ID {
			continue
		}
		if prior, ok := roster[v.ID]; ok && flat[v.ID] && prior.StrategyID == v.StrategyID && prior.GateID == v.GateID {
			continue
		}
		needed = append(needed, v)
	}
	return needed, nil
}

func checkPortfolioAdmissions(s account.CappedState, c account.BookCommand, read func(string) (strategygate.Record, error), total func(string) (int64, error), readForward func(string) (forward.Record, error), readPlan func(string) (forward.AssessmentPlanRecord, error)) error {
	if e := checkQualificationCollection(s, c, readForward, readPlan); e != nil {
		return e
	}
	if e := checkForwardBindings(s, c, readForward, total); e != nil {
		return e
	}
	needed, e := neededAdmissions(s, c)
	if e != nil {
		return e
	}
	for _, v := range needed {
		g, e := read(v.GateID)
		if e != nil {
			return e
		}
		if g.Decision == nil || g.Decision.Stage != "incubation" || g.Decision.StrategyID != v.StrategyID || g.Declaration.Account != string(s.Config.Type) || g.Decision.Minimum != v.Minimum {
			return errors.New("incubation_gate_required")
		}
		if !g.Decision.AdmitsIncubation() {
			return errors.New("gate_evidence_ineligible")
		}
		if account.IsStrategyPlan(c) {
			cost, err := executioncost.FromDeclaration(g.Declaration.BaseCost, g.Decision.Status == "forward_only")
			if err != nil {
				return err
			}
			if c.Incubation.Costs[v.ID] != cost {
				return errors.New("incubation cost model differs from gate")
			}
		}
		expected := "incubation"
		model := false
		if g.Decision.Status == "forward_only" {
			expected, model = "forward_only", true
		} else if g.Decision.Status != "passed" {
			return errors.New("incubation_gate_required")
		}

		n, e := total(g.Declaration.Family)
		if e != nil {
			return e
		}
		if v.Stage == "qualified" {
			q := account.Qualification(s, v.ID)
			if q == nil || q.StrategyID != v.StrategyID || q.SearchTotal != n {
				return errors.New("forward_qualification_required_or_family_changed")
			}
			expected = "qualified"
		}
		if v.Stage != expected || v.ModelDriven != model {
			return errors.New("gate_stage_changed")
		}
		if v.Stage != "qualified" && n != g.Decision.SearchTotal {
			return errors.New("gate_search_history_changed")
		}
	}
	return nil
}

func (m *Memory) checkPortfolioAdmissionsLocked(s account.CappedState, c account.BookCommand) error {
	e := checkPortfolioAdmissions(s, c, func(id string) (strategygate.Record, error) {
		g, ok := m.strategyGates[id]
		if !ok {
			return g, strategygate.ErrNotFound
		}
		return g, nil
	}, func(family string) (int64, error) {
		return m.strategySearchTotalLocked(family)
	}, func(id string) (forward.Record, error) {
		r, ok := m.forwardDeclarations[id]
		if !ok {
			return r, forward.ErrNotFound
		}
		return r, m.validateForwardLocked(r)
	}, func(id string) (forward.AssessmentPlanRecord, error) {
		r, ok := m.forwardAssessmentPlans[id]
		if !ok {
			return r, forward.ErrNotFound
		}
		return r, validateAssessmentPlan(r, m.forwardDeclarations[r.Plan.DeclarationID])
	})
	if e != nil {
		return e
	}
	return validateQualifiedAdmissions(context.Background(), s, c, m.qualificationEvaluator, func(id string) (forward.AssessmentPlanRecord, forward.Record, strategygate.Record, []strategytrial.Start, error) {
		p, ok := m.forwardAssessmentPlans[id]
		if !ok {
			return p, forward.Record{}, strategygate.Record{}, nil, forward.ErrNotFound
		}
		w := m.forwardDeclarations[p.Plan.DeclarationID]
		g := m.strategyGates[w.Declaration.GateID]
		return p, w, g, sortedStarts(m.strategyTrials, w.Declaration.Family), nil
	})
}

func lockStrategyFamily(ctx context.Context, tx pgx.Tx, family string) error {
	_, e := tx.Exec(ctx, "SELECT pg_advisory_xact_lock(hashtextextended('mantik_strategy_family:'||$1,0))", family)
	return e
}

func (p *Postgres) checkPortfolioAdmissionsTx(ctx context.Context, tx pgx.Tx, s account.CappedState, c account.BookCommand) error {
	// All portfolio writers take the book row first, then sorted family locks.
	// Trial starts take a family lock only, so no inverse book/family order exists.
	needed, e := neededAdmissions(s, c)
	if e != nil {
		return e
	}
	families := []string{}
	for _, v := range needed {
		g, e := loadGate(ctx, tx, v.GateID)
		if e != nil {
			return e
		}
		families = append(families, g.Declaration.Family)
	}
	slices.Sort(families)
	families = slices.Compact(families)
	for _, f := range families {
		if e = lockStrategyFamily(ctx, tx, f); e != nil {
			return e
		}
	}
	e = checkPortfolioAdmissions(s, c, func(id string) (strategygate.Record, error) { return loadGate(ctx, tx, id) }, func(family string) (int64, error) {
		return strategySearchTotal(ctx, tx, family)
	}, func(id string) (forward.Record, error) { return p.loadForward(ctx, tx, id) }, func(id string) (forward.AssessmentPlanRecord, error) { return p.loadAssessmentPlan(ctx, tx, id) })
	if e != nil {
		return e
	}
	return validateQualifiedAdmissions(ctx, s, c, p.qualificationEvaluator, func(id string) (forward.AssessmentPlanRecord, forward.Record, strategygate.Record, []strategytrial.Start, error) {
		plan, e := p.loadAssessmentPlan(ctx, tx, id)
		if e != nil {
			return plan, forward.Record{}, strategygate.Record{}, nil, e
		}
		w, e := p.loadForward(ctx, tx, plan.Plan.DeclarationID)
		if e != nil {
			return plan, w, strategygate.Record{}, nil, e
		}
		g, e := loadGate(ctx, tx, w.Declaration.GateID)
		if e != nil {
			return plan, w, g, nil, e
		}
		starts, e := loadQualificationStarts(ctx, tx, w.Declaration.Family)
		return plan, w, g, starts, e
	})
}

func checkQualificationCollection(s account.CappedState, c account.BookCommand, read func(string) (forward.Record, error), readPlan func(string) (forward.AssessmentPlanRecord, error)) error {
	if c.Incubation == nil || c.Incubation.Qualification == nil {
		return nil
	}
	p, e := readPlan(c.Incubation.Qualification.PlanID)
	if e != nil {
		return e
	}
	w, e := read(p.Plan.DeclarationID)
	if e != nil {
		return e
	}
	d := w.Declaration
	if len(c.Incubation.Qualification.DecisionSources) != len(c.Incubation.Decisions) {
		return forward.ErrInvalid
	}
	if c.Kind != "qualification_plan" || d.BookID != s.Config.ID || c.Incubation.DecidedAt.Before(d.WindowStart) || !c.At.Before(d.OutcomeDeadline) || c.Incubation.Qualification.Benchmark.Validate(c.At) != nil || c.Incubation.Qualification.Benchmark.StrategyID != d.Benchmark.StrategyID || len(c.Incubation.Rebalance.Sleeves) != 1 {
		return forward.ErrInvalid
	}
	v := c.Incubation.Rebalance.Sleeves[0]
	if v.ID != d.SleeveID || v.StrategyID != d.StrategyID || v.GateID != d.GateID || c.Incubation.ForwardDeclarations[v.ID] != d.ID {
		return forward.ErrConflict
	}
	for _, event := range s.Events {
		if event.Incubation != nil {
			for _, owner := range event.Incubation.Rebalance.Sleeves {
				if owner.ID == d.SleeveID && (event.Kind != "qualification_plan" || event.Incubation.Qualification == nil || event.Incubation.Qualification.PlanID != p.Plan.ID) {
					return errors.New("qualification_requires_separate_new_window_owner")
				}
			}
		}
	}
	return nil
}
