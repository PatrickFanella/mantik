package storage

import (
	"math/big"
	"slices"

	"git.subcult.tv/subculture-collective/mantik/internal/paperaccount"
	"git.subcult.tv/subculture-collective/mantik/internal/paperstrategy"
)

const PaperSensitivityEvaluationRecipe = "native-threshold-sensitivity-plan-v9"
const PaperSensitivityAssessmentRecipe = "native-predeclared-threshold-sensitivity-v14"
const PaperThresholdSensitivityMethod = "native-threshold-neighbor-total-return-v1"

// PaperEvaluationSensitivityPlan names, before observations, the declared
// variants whose entry thresholds bracket each assessed variant. Every member
// of a neighborhood must reach MinimumNeighborNetCents of total-return net in
// the base and the stress model; the stress model already costs at least as
// much as base on every term.
type PaperEvaluationSensitivityPlan struct {
	Method                  string                        `json:"method"`
	MinimumNeighborNetCents int64                         `json:"minimum_neighbor_net_cents"`
	Neighborhoods           []PaperEvaluationNeighborhood `json:"neighborhoods"`
}

type PaperEvaluationNeighborhood struct {
	StrategyID  string   `json:"strategy_id"`
	NeighborIDs []string `json:"neighbor_ids"`
}

// validSensitivityPlan checks the plan's shape against the declared variants.
// Threshold bracketing needs the registered definitions and is checked by
// sensitivityNeighborhoodTerms when the declaration is issued.
func validSensitivityPlan(c PaperEvaluationDeclarationCommand) bool {
	p := c.Plan.Sensitivity
	if p == nil || p.Method != PaperThresholdSensitivityMethod || c.Plan.TotalReturnMethod == "" || p.MinimumNeighborNetCents < 0 || p.MinimumNeighborNetCents > paperaccount.MaxOpeningCashCents || len(p.Neighborhoods) < 1 || len(p.Neighborhoods) > len(c.Plan.Variants) {
		return false
	}
	declared := map[string]bool{}
	for _, v := range c.Plan.Variants {
		declared[v.ID] = true
	}
	for i, n := range p.Neighborhoods {
		if !declared[n.StrategyID] || i > 0 && p.Neighborhoods[i-1].StrategyID >= n.StrategyID || len(n.NeighborIDs) < 2 || len(n.NeighborIDs) >= paperstrategy.MaxDefinitions {
			return false
		}
		for j, id := range n.NeighborIDs {
			if !declared[id] || id == n.StrategyID || j > 0 && n.NeighborIDs[j-1] >= id {
				return false
			}
		}
	}
	return true
}

// Neighbors differ from their center only in the entry threshold, and at least
// one lies on each side of it.
func sensitivityNeighborhoodTerms(p *PaperEvaluationSensitivityPlan, variants []paperstrategy.Record) error {
	if p == nil {
		return nil
	}
	byID := map[string]paperstrategy.Definition{}
	for _, v := range variants {
		byID[v.Command.Definition.ID] = v.Command.Definition
	}
	for _, n := range p.Neighborhoods {
		center, below, above := byID[n.StrategyID], false, false
		for _, id := range n.NeighborIDs {
			d := byID[id]
			if d.Method != center.Method || d.Symbol != center.Symbol || d.Feed != center.Feed || d.InputEvidenceClass != center.InputEvidenceClass || d.Quantity != center.Quantity || d.Safety != center.Safety {
				return ErrPaperEvaluationDeclarationConflict
			}
			below = below || d.EntryAtOrBelowCents < center.EntryAtOrBelowCents
			above = above || d.EntryAtOrBelowCents > center.EntryAtOrBelowCents
		}
		if !below || !above {
			return ErrPaperEvaluationDeclarationConflict
		}
	}
	return nil
}

// applyThresholdSensitivity decides parameter_and_cost_sensitivity from the
// retained strategy total returns already validated by the benchmark step.
func applyThresholdSensitivity(r *PaperEvaluationAssessment, p *PaperEvaluationSensitivityPlan) error {
	if r.Portfolio == nil || p == nil {
		return ErrPaperEvaluationAssessmentInvalid
	}
	rows := map[string]PaperEvaluationPortfolioVariant{}
	for _, v := range r.Portfolio.Variants {
		rows[v.StrategyID] = v
	}
	members := map[string][]string{}
	for _, n := range p.Neighborhoods {
		members[n.StrategyID] = append([]string{n.StrategyID}, n.NeighborIDs...)
	}
	floor := new(big.Rat).SetInt64(p.MinimumNeighborNetCents)
	for i := range r.Checks {
		check := &r.Checks[i]
		if check.Name != "parameter_and_cost_sensitivity" {
			continue
		}
		ids, ok := members[check.StrategyID]
		check.Status = "unavailable"
		if !ok {
			check.ReasonCode = "threshold_neighbors_not_declared"
			continue
		}
		nets := []*big.Rat{}
		for _, id := range ids {
			v := rows[id]
			for _, t := range []*PaperEvaluationTotalReturn{v.BaseTotalReturn, v.StressTotalReturn} {
				if t == nil || !t.Strategy.Available() {
					nets = nil
					break
				}
				net, ok := exactCents(t.Strategy.NetCents)
				if !ok {
					return ErrPaperEvaluationAssessmentInvalid
				}
				nets = append(nets, net)
			}
			if nets == nil {
				break
			}
		}
		switch {
		case nets == nil:
			check.ReasonCode = "threshold_neighborhood_total_return_unavailable"
		case r.Evidence.Phase != "outcome_deadline_elapsed":
			check.Status, check.ReasonCode = "pending", "predeclared_outcome_window_not_closed"
		case slices.ContainsFunc(nets, func(x *big.Rat) bool { return x.Cmp(floor) < 0 }):
			check.Status, check.ReasonCode = "failed", "threshold_neighborhood_net_below_predeclared_minimum"
			r.Status = "blocked"
		default:
			check.Status, check.ReasonCode = "passed", "threshold_neighborhood_meets_predeclared_net_in_base_and_stress"
		}
	}
	return nil
}
