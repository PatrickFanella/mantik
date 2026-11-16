package storage

import (
	"context"
	"errors"
	"slices"

	"git.subcult.tv/subculture-collective/mantik/internal/account"
	"git.subcult.tv/subculture-collective/mantik/internal/allocation"
	"git.subcult.tv/subculture-collective/mantik/internal/strategygate"
	"github.com/jackc/pgx/v5"
)

func admissionRequest(s account.CappedState, c account.BookCommand) (*allocation.RebalanceRequest, error) {
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

func checkPortfolioAdmissions(s account.CappedState, c account.BookCommand, read func(string) (strategygate.Record, error), total func(string) (int64, error)) error {
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
		expected := "incubation"
		model := false
		if g.Decision.Status == "forward_only" {
			expected, model = "forward_only", true
		} else if g.Decision.Status != "passed" {
			return errors.New("incubation_gate_required")
		}
		if v.Stage != expected || v.ModelDriven != model {
			return errors.New("gate_stage_changed")
		}
		n, e := total(g.Declaration.Family)
		if e != nil {
			return e
		}
		if n != g.Decision.SearchTotal {
			return errors.New("gate_search_history_changed")
		}
	}
	return nil
}

func (m *Memory) checkPortfolioAdmissionsLocked(s account.CappedState, c account.BookCommand) error {
	return checkPortfolioAdmissions(s, c, func(id string) (strategygate.Record, error) {
		g, ok := m.strategyGates[id]
		if !ok {
			return g, strategygate.ErrNotFound
		}
		return g, nil
	}, func(family string) (int64, error) {
		var n int64
		for _, r := range m.strategyTrials {
			if r.Start.Family == family {
				n++
			}
		}
		return n, nil
	})
}

func lockStrategyFamily(ctx context.Context, tx pgx.Tx, family string) error {
	_, e := tx.Exec(ctx, "SELECT pg_advisory_xact_lock(hashtextextended('mantik_strategy_family:'||$1,0))", family)
	return e
}

func checkPortfolioAdmissionsTx(ctx context.Context, tx pgx.Tx, s account.CappedState, c account.BookCommand) error {
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
	return checkPortfolioAdmissions(s, c, func(id string) (strategygate.Record, error) { return loadGate(ctx, tx, id) }, func(family string) (int64, error) {
		var n int64
		e := tx.QueryRow(ctx, `SELECT count(*) FROM mantik_strategy_trials WHERE family=$1`, family).Scan(&n)
		return n, e
	})
}
