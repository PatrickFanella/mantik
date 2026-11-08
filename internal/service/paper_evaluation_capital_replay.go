package service

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"strings"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/paperaccount"
	"git.subcult.tv/subculture-collective/mantik/internal/paperfill"
	"git.subcult.tv/subculture-collective/mantik/internal/storage"
	"git.subcult.tv/subculture-collective/mantik/internal/workerplan"
)

// This study budget is not an operational reservation or admission. Cumulative
// allocation never reuses released/earned cash within the declared window.
const capitalReplayMethod = "opening-capital-no-reinvestment-counterfactual-v1"

type PaperEvaluationCapitalReplay struct {
	Schema                      string                          `json:"schema"`
	Method                      string                          `json:"method"`
	EntryEvidence               PaperEvaluationEntryReplay      `json:"entry_evidence"`
	AccountSHA256               string                          `json:"account_sha256"`
	OpeningCashCents            int64                           `json:"opening_cash_cents"`
	Variants                    []PaperEvaluationCapitalVariant `json:"variants"`
	Authority                   string                          `json:"authority"`
	PortfolioEvaluationComplete bool                            `json:"portfolio_evaluation_complete"`
	AdmissionAvailable          bool                            `json:"admission_available"`
	ExecutionAvailable          bool                            `json:"execution_available"`
	QualificationCredit         bool                            `json:"qualification_credit"`
	SHA256                      string                          `json:"sha256"`
}
type PaperEvaluationCapitalVariant struct {
	Exclusions []PaperEvaluationEntryExclusion `json:"exclusions"`
	StrategyID string                          `json:"strategy_id"`
	Status     string                          `json:"status"`
	Base       *PaperEvaluationCapitalModel    `json:"base,omitempty"`
	Stress     *PaperEvaluationCapitalModel    `json:"stress,omitempty"`
}
type PaperEvaluationCapitalCandidate struct {
	DecisionID                string    `json:"decision_id"`
	DecisionSHA256            string    `json:"decision_sha256"`
	At                        time.Time `json:"at"`
	EntryDebitCeilingCents    int64     `json:"entry_debit_ceiling_cents"`
	ExitShortfallCeilingCents int64     `json:"exit_shortfall_ceiling_cents"`
	TotalFundingCents         int64     `json:"total_funding_cents"`
	Status                    string    `json:"status"`
	Reasons                   []string  `json:"reasons"`
}
type PaperEvaluationCapitalModel struct {
	CashBenchmarkMethod         string                            `json:"cash_benchmark_method"`
	MarkedEquityAvailable       bool                              `json:"marked_equity_available"`
	ModelSHA256                 string                            `json:"model_sha256"`
	Candidates                  []PaperEvaluationCapitalCandidate `json:"candidates"`
	CumulativeFundingCents      int64                             `json:"cumulative_funding_cents"`
	UnallocatedOpeningCashCents int64                             `json:"unallocated_opening_cash_cents"`
	EntryDebitCents             int64                             `json:"entry_debit_cents"`
	ExitNetProceedsCents        int64                             `json:"exit_net_proceeds_cents"`
	EndingCashCents             int64                             `json:"ending_cash_cents"`
	RemainingCostBasisCents     int64                             `json:"remaining_cost_basis_cents"`
	RealizedNetCents            int64                             `json:"realized_net_cents"`
	ClosedNetCents              *int64                            `json:"closed_net_cents,omitempty"`
	CashBenchmarkNetCents       int64                             `json:"cash_benchmark_net_cents"`
	NetExcessOverCashCents      *int64                            `json:"net_excess_over_cash_cents,omitempty"`
	PassiveBenchmarkAvailable   bool                              `json:"passive_benchmark_available"`
	DrawdownAvailable           bool                              `json:"drawdown_available"`
	ConservationVerified        bool                              `json:"conservation_verified"`
	Replay                      *PaperEvaluationProtectiveModel   `json:"replay,omitempty"`
}

func inspectEvaluationCapitalReplay(ctx context.Context, s storage.PaperEvaluationEntrySources) (PaperEvaluationCapitalReplay, error) {
	if err := ctx.Err(); err != nil {
		return PaperEvaluationCapitalReplay{}, err
	}
	entry, err := inspectEvaluationEntryReplay(ctx, s)
	if err != nil {
		return PaperEvaluationCapitalReplay{}, err
	}
	if s.Policy.Validate() != nil || s.Policy.Account.Config.OpeningCashCents < 1 {
		return PaperEvaluationCapitalReplay{}, storage.ErrPaperEvaluationEvidenceInvalid
	}
	out := PaperEvaluationCapitalReplay{Schema: "mantik-owned-evaluation-capital-replay-v1", Method: capitalReplayMethod, EntryEvidence: entry, AccountSHA256: s.Policy.Account.SHA256, OpeningCashCents: s.Policy.Account.Config.OpeningCashCents, Variants: []PaperEvaluationCapitalVariant{}, Authority: "service_owned_finite_capital_counterfactual_only"}
	for _, v := range entry.Variants {
		result := PaperEvaluationCapitalVariant{StrategyID: v.StrategyID, Status: v.Status, Exclusions: []PaperEvaluationEntryExclusion{}}
		if v.Base != nil && v.Stress != nil {
			sources := []storage.PaperEvaluationEntrySource{}
			markets := []PaperOutcomeMarketEvidence{}
			for i, r := range s.Rows {
				if r.Strategy.Command.Definition.ID == v.StrategyID {
					sources = append(sources, r)
					markets = append(markets, *entry.Rows[i].Market)
				}
			}
			commands, cut, excluded, err := protectiveReplayQuotes(ctx, sources, markets)
			if err != nil {
				return PaperEvaluationCapitalReplay{}, err
			}
			result.Exclusions = excluded
			result.Base, err = replayCapitalModel(ctx, s, sources, commands, cut, *s.Policy.Command.ExecutionModel, false)
			if err == nil {
				result.Stress, err = replayCapitalModel(ctx, s, sources, commands, cut, s.Evidence.Declaration.Command.Plan.StressModel, true)
			}
			result.Status = "finite_capital_counterfactual_only_evaluation_unavailable"
			if errors.Is(err, paperfill.ErrCapacity) {
				result.Base = nil
				result.Stress = nil
				result.Status = "capital_replay_capacity_exceeded"
			} else if err != nil {
				return PaperEvaluationCapitalReplay{}, err
			}
		}
		out.Variants = append(out.Variants, result)
	}
	out.SHA256 = workerplan.Hash(out)
	raw, err := json.Marshal(out)
	if err != nil || len(raw) > storage.MaxPaperEvaluationEvidenceBytes {
		return PaperEvaluationCapitalReplay{}, storage.ErrPaperEvaluationEvidenceInvalid
	}
	return out, nil
}

// Native variants are alternative same-strategy studies. Funding is selected in
// original decision-clock/ID order, using only the actual policy's finite limits.
// No allocation.Admission or caller snapshot is constructed to bypass a gate.
func replayCapitalModel(ctx context.Context, s storage.PaperEvaluationEntrySources, sources []storage.PaperEvaluationEntrySource, quotes []paperfill.BookCommand, cut time.Time, model paperfill.Model, stress bool) (*PaperEvaluationCapitalModel, error) {

	if s.Policy.Command.ExecutionModel == nil {
		return nil, paperfill.ErrInvalid
	}
	expected := *s.Policy.Command.ExecutionModel
	if stress {
		expected = s.Evidence.Declaration.Command.Plan.StressModel
	}
	if expected != model {
		return nil, paperfill.ErrInvalid
	}
	opening := s.Policy.Account.Config.OpeningCashCents
	if s.Policy.Validate() != nil || opening < 1 || opening > paperfill.MaxMoneyCents || len(sources) == 0 {
		return nil, paperfill.ErrInvalid
	}
	out := &PaperEvaluationCapitalModel{CashBenchmarkMethod: "nominal-usd-cash-no-interest-v1", ModelSHA256: workerplan.Hash(model), Candidates: []PaperEvaluationCapitalCandidate{}, UnallocatedOpeningCashCents: opening, EndingCashCents: opening}
	ordered := append([]storage.PaperEvaluationEntrySource{}, sources...)
	slices.SortFunc(ordered, func(a, b storage.PaperEvaluationEntrySource) int {
		if c := a.Decision.FinalizedAt.Compare(b.Decision.FinalizedAt); c != 0 {
			return c
		}
		return strings.Compare(a.Decision.Command.ID, b.Decision.Command.ID)
	})
	strategyUsed := map[string]int64{}
	symbolUsed := map[string]int64{}
	groupUsed := map[string]int64{}
	selected := []storage.PaperEvaluationEntrySource{}
	for _, r := range ordered {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		cost := r.BaseCost
		if stress {
			cost = r.StressCost
		}
		if cost == nil || cost.Quantity < 1 || cost.MaxDebitCents < 1 || cost.ModelSHA256 != workerplan.Hash(model) {
			return nil, paperfill.ErrInvalid
		}
		shortfall, err := paperfill.ExitShortfallCeiling(model, cost.Quantity)
		if err != nil || cost.MaxDebitCents > paperfill.MaxMoneyCents-shortfall {
			return nil, paperfill.ErrInvalid
		}
		funding := cost.MaxDebitCents + shortfall
		c := PaperEvaluationCapitalCandidate{DecisionID: r.Decision.Command.ID, DecisionSHA256: r.Decision.SHA256, At: r.Decision.FinalizedAt, EntryDebitCeilingCents: cost.MaxDebitCents, ExitShortfallCeilingCents: shortfall, TotalFundingCents: funding, Status: "not_selected_for_counterfactual", Reasons: []string{}}
		strategy, symbol := r.Strategy.Command.Definition.ID, r.Strategy.Command.Definition.Symbol
		strategyLimit, symbolLimit := int64(0), int64(0)
		groups := []string{}
		for _, rule := range s.Policy.Command.Policy.Strategies {
			if rule.ID == strategy && rule.ArtifactSHA256 == r.Strategy.DefinitionSHA256 {
				strategyLimit = rule.LimitCents
			}
		}
		for _, rule := range s.Policy.Command.Policy.Symbols {
			if rule.ID == symbol {
				symbolLimit = rule.LimitCents
				groups = rule.Groups
			}
		}
		if strategyLimit == 0 || symbolLimit == 0 {
			return nil, paperfill.ErrInvalid
		}
		if funding > s.Policy.Account.Config.CapitalLimitCents-out.CumulativeFundingCents {
			c.Reasons = append(c.Reasons, "account_capital_limit_exceeded")
		}
		if funding > out.UnallocatedOpeningCashCents {
			c.Reasons = append(c.Reasons, "opening_cash_budget_exceeded")
		}
		if funding > strategyLimit-strategyUsed[strategy] {
			c.Reasons = append(c.Reasons, "strategy_budget_exceeded")
		}
		if funding > symbolLimit-symbolUsed[symbol] {
			c.Reasons = append(c.Reasons, "symbol_budget_exceeded")
		}
		for _, id := range groups {
			limit := int64(0)
			for _, g := range s.Policy.Command.Policy.Groups {
				if g.ID == id {
					limit = g.LimitCents
				}
			}
			if limit == 0 {
				return nil, paperfill.ErrInvalid
			}
			if funding > limit-groupUsed[id] {
				c.Reasons = append(c.Reasons, "group_budget_exceeded:"+id)
			}
		}
		if len(c.Reasons) == 0 {
			c.Status = "selected_for_counterfactual_only"
			selected = append(selected, r)
			out.CumulativeFundingCents += funding
			out.UnallocatedOpeningCashCents -= funding
			strategyUsed[strategy] += funding
			symbolUsed[symbol] += funding
			for _, g := range groups {
				groupUsed[g] += funding
			}
		}
		out.Candidates = append(out.Candidates, c)
	}
	if len(selected) > 0 {
		// Rejected candidates must not consume displayed capacity in the funded path.
		// Earlier quotes can precede the first selected order after funding rejection.
		first := selected[0].Decision.FinalizedAt.Add(time.Nanosecond)
		commands := []paperfill.BookCommand{}
		for _, q := range quotes {
			if !q.At.Before(first) {
				commands = append(commands, q)
			}
		}
		replay, err := replayProtectiveModel(ctx, selected, commands, cut, model, stress)
		if err != nil {
			return nil, err
		}
		out.Replay = replay
		complete := true
		for _, p := range replay.Positions {
			if p.EnteredQuantity < 1 || p.ClosedQuantity < 0 || p.ClosedQuantity > p.EnteredQuantity || p.EntryDebitCents < 0 {
				return nil, paperfill.ErrInvalid
			}
			// Match the ledger's cumulative whole-share cost allocation: floor closed
			// basis, leaving the final share the remaining rounding cents.
			closedBasis := p.EntryDebitCents/p.EnteredQuantity*p.ClosedQuantity + (p.EntryDebitCents%p.EnteredQuantity)*p.ClosedQuantity/p.EnteredQuantity
			out.EntryDebitCents += p.EntryDebitCents
			out.ExitNetProceedsCents += p.ExitNetProceedsCents
			out.RemainingCostBasisCents += p.EntryDebitCents - closedBasis
			out.RealizedNetCents += p.ExitNetProceedsCents - closedBasis
			if p.UnclosedQuantity != 0 {
				complete = false
			}
		}
		out.EndingCashCents = opening - out.EntryDebitCents + out.ExitNetProceedsCents
		if complete {
			net := out.EndingCashCents - opening
			out.ClosedNetCents = &net
			excess := net
			out.NetExcessOverCashCents = &excess
		}
	} else {
		zero := int64(0)
		out.ClosedNetCents = &zero
		other := int64(0)
		out.NetExcessOverCashCents = &other
	}
	if out.CumulativeFundingCents > opening || out.RealizedNetCents < -paperaccount.MaxOpeningCashCents || out.RealizedNetCents > paperaccount.MaxOpeningCashCents || out.EndingCashCents < 0 || out.EndingCashCents > 2*paperaccount.MaxOpeningCashCents || out.EntryDebitCents > out.CumulativeFundingCents || out.EndingCashCents+out.RemainingCostBasisCents != opening+out.RealizedNetCents {
		return nil, paperfill.ErrInvalid
	}
	out.ConservationVerified = true
	return out, nil
}
func paperEvaluationCapitalReplayRoutes(mux *http.ServeMux, store storage.Store, clock Clock) {
	backend, ok := store.(storage.PaperEvaluationEntrySourceStore)
	if !ok {
		return
	}
	mux.HandleFunc("GET /v1/paper-evaluation-declarations/{id}/capital-replay", func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		if !paperaccount.ID.MatchString(id) || r.URL.RawQuery != "" || r.URL.ForceQuery || r.ContentLength != 0 || len(r.TransferEncoding) > 0 {
			write(w, 422, map[string]string{"error": "invalid capital replay query"})
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), raceDeadlineScale*5*time.Second)
		defer cancel()
		s, err := backend.PaperEvaluationEntrySources(ctx, id, clock.Now, evaluateOwnedPaperDecision, evaluateOwnedPaperOutcome)
		if errors.Is(err, storage.ErrPaperEvaluationDeclarationNotFound) {
			write(w, 404, map[string]string{"error": "paper evaluation declaration not found"})
			return
		}
		if err == nil {
			var out PaperEvaluationCapitalReplay
			out, err = inspectEvaluationCapitalReplay(ctx, s)
			if err == nil {
				write(w, 200, out)
				return
			}
		}
		write(w, 503, map[string]string{"error": "owned capital replay unavailable"})
	})
}
