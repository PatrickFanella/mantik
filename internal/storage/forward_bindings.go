package storage

import (
	"errors"

	"git.subcult.tv/subculture-collective/mantik/internal/account"
	"git.subcult.tv/subculture-collective/mantik/internal/allocation"
	"git.subcult.tv/subculture-collective/mantik/internal/forward"
)

func checkForwardBindings(s account.CappedState, c account.BookCommand, read func(string) (forward.Record, error), total func(string) (int64, error)) error {
	req, e := admissionRequest(s, c)
	if e != nil || req == nil {
		return e
	}
	bindings := account.ForwardBindings(s)
	if c.Incubation != nil {
		for sleeve, id := range c.Incubation.ForwardDeclarations {
			if id == "" || bindings[sleeve] != "" && bindings[sleeve] != id {
				return forward.ErrConflict
			}
			found := false
			for _, v := range req.Sleeves {
				if v.ID == sleeve {
					found = true
				}
			}
			if !found {
				return forward.ErrInvalid
			}
			bindings[sleeve] = id
		}
	}
	for _, v := range req.Sleeves {
		if c.Kind == "portfolio_reserve" && c.Order.SleeveID != v.ID {
			continue
		}
		id := bindings[v.ID]
		if id == "" {
			continue
		}
		r, e := read(id)
		if e != nil {
			return e
		}
		d := r.Declaration
		if d.BookID != s.Config.ID || d.SleeveID != v.ID || d.StrategyID != v.StrategyID || d.GateID != v.GateID {
			return forward.ErrConflict
		}
		if v.Stage == "qualified" {
			continue
		}
		// Actual reduce-only reservations remain available after expiry/search growth.
		if c.Kind == "portfolio_reserve" && c.Order.ReduceOnly {
			continue
		}
		var target allocation.TargetDecision
		found := false
		for _, t := range req.Targets {
			if t.SleeveID == v.ID {
				target = t.Decision
				found = true
			}
		}
		if !found {
			return forward.ErrInvalid
		}
		if allocation.FlatTarget(target) {
			continue
		}
		n, e := total(d.Family)
		if e != nil {
			return e
		}
		opened, e := account.ForwardHoldingOpened(s, v.ID, c.At)
		if e != nil {
			return e
		}
		clocks := r.Clocks(c.At, n, opened)
		if c.Incubation != nil {
			if c.Incubation.DecidedAt.Before(r.DeclaredAt) {
				return forward.ErrConflict
			}
			decision := r.Clocks(c.Incubation.DecidedAt, n, opened)
			clocks.EntryEligible = clocks.EntryEligible && decision.EntryEligible
		}
		if !opened.IsZero() && opened.Before(d.WindowStart) {
			return errors.New("forward exposure predates window; flatten first")
		}
		if !clocks.OutcomeOpen || clocks.HoldExpired || !clocks.EntryEligible && !target.HoldExposure {
			return errors.New("forward clocks or search bound expired; flatten required")
		}
	}
	return nil
}
