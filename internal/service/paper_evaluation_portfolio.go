package service

import (
	"context"
	"errors"

	"git.subcult.tv/subculture-collective/mantik/internal/papereconomics"
	"git.subcult.tv/subculture-collective/mantik/internal/paperfill"
	"git.subcult.tv/subculture-collective/mantik/internal/storage"
	"git.subcult.tv/subculture-collective/mantik/internal/workerplan"
)

// EvaluateOwnedPaperPortfolio is the compiled constructor dependency for the
// assessment owner. The owner supplies its replayed transaction snapshot; no
// public command can provide books, calculations, balances or evaluation flags.
func EvaluateOwnedPaperPortfolio(ctx context.Context, inputs storage.PaperEvaluationEntrySources) (storage.PaperEvaluationPortfolio, error) {
	if inputs.AccountingRecipe != "" && inputs.AccountingRecipe != storage.PaperBenchmarkAssessmentRecipe && inputs.AccountingRecipe != storage.PaperTotalReturnAssessmentRecipe && inputs.AccountingRecipe != storage.PaperStatisticalAssessmentRecipe {
		return storage.PaperEvaluationPortfolio{}, storage.ErrPaperEvaluationAssessmentInvalid
	}
	decisions := inputs.AccountingRecipe == storage.PaperStatisticalAssessmentRecipe
	totalReturn := inputs.AccountingRecipe == storage.PaperTotalReturnAssessmentRecipe || decisions
	benchmarks := inputs.AccountingRecipe == storage.PaperBenchmarkAssessmentRecipe || totalReturn
	// An empty list (no covered instrument) does not survive cloning as non-nil.
	if !totalReturn && len(inputs.CorporateActions) > 0 || totalReturn && inputs.Evidence.Declaration.Command.Plan.TotalReturnMethod != papereconomics.TotalReturnMethod {
		return storage.PaperEvaluationPortfolio{}, storage.ErrPaperEvaluationAssessmentInvalid
	}
	actions := map[string]papereconomics.Actions{}
	for _, a := range inputs.CorporateActions {
		if _, dup := actions[a.Symbol]; dup {
			return storage.PaperEvaluationPortfolio{}, storage.ErrPaperEvaluationAssessmentInvalid
		}
		actions[a.Symbol] = a
	}
	if benchmarks && inputs.Evidence.Declaration.Command.Plan.BenchmarkMethod != papereconomics.BenchmarkMethod {
		return storage.PaperEvaluationPortfolio{}, storage.ErrPaperEvaluationAssessmentInvalid
	}
	capital, err := inspectEvaluationCapitalReplay(withSessionMemo(ctx), inputs)
	if err != nil {
		return storage.PaperEvaluationPortfolio{}, err
	}
	out := storage.PaperEvaluationPortfolio{Method: storage.PaperPortfolioMethod, EvidenceSHA256: inputs.Evidence.SHA256, SourceSHA256: workerplan.Hash(inputs), PolicySHA256: inputs.Policy.SHA256, Variants: []storage.PaperEvaluationPortfolioVariant{}, Authority: "service_owned_observed_portfolio_accounting_only"}
	if benchmarks {
		out.Method = storage.PaperBenchmarkPortfolioMethod
	}
	if totalReturn {
		out.Method = storage.PaperTotalReturnPortfolioMethod
	}
	if decisions {
		out.Method = storage.PaperDecisionReturnPortfolioMethod
	}
	for _, v := range capital.Variants {
		row := storage.PaperEvaluationPortfolioVariant{StrategyID: v.StrategyID, Status: v.Status}
		if v.Base != nil && v.Stress != nil && v.Base.Replay != nil && v.Stress.Replay != nil {
			values := []*papereconomics.Result{}
			baselines := []*papereconomics.Benchmarks{}
			returns := []*storage.PaperEvaluationTotalReturn{}
			for _, model := range []*PaperEvaluationCapitalModel{v.Base, v.Stress} {
				p := model.Replay
				value, err := papereconomics.Evaluate(ctx, papereconomics.Input{OpeningCashCents: capital.OpeningCashCents, EntryBook: p.EntryBook, ExitBook: p.ExitBook, CutAt: p.CutAt})
				if err != nil {
					return storage.PaperEvaluationPortfolio{}, err
				}
				if value.EndingCashCents != model.EndingCashCents || value.RemainingCostCents != model.RemainingCostBasisCents || value.RealizedNetCents != model.RealizedNetCents {
					return storage.PaperEvaluationPortfolio{}, storage.ErrPaperEvaluationAssessmentInvalid
				}
				values = append(values, &value)
				if benchmarks {
					baseline, e := papereconomics.EvaluateBenchmarks(ctx, papereconomics.Input{OpeningCashCents: capital.OpeningCashCents, EntryBook: p.EntryBook, ExitBook: p.ExitBook, CutAt: p.CutAt})
					if e != nil {
						return storage.PaperEvaluationPortfolio{}, e
					}
					baselines = append(baselines, &baseline)
				}
				// Only a covered instrument has reconciled actions to apply.
				if a, ok := actions[p.EntryBook.Orders[0].Symbol]; ok && totalReturn {
					in := papereconomics.Input{OpeningCashCents: capital.OpeningCashCents, EntryBook: p.EntryBook, ExitBook: p.ExitBook, CutAt: p.CutAt}
					strategy, e := papereconomics.EvaluateTotalReturn(ctx, in, a)
					if e != nil {
						return storage.PaperEvaluationPortfolio{}, e
					}
					t := &storage.PaperEvaluationTotalReturn{Strategy: strategy}
					if b := baselines[len(baselines)-1]; b.PassiveBook != nil {
						passive, e := papereconomics.EvaluateTotalReturn(ctx, papereconomics.Input{OpeningCashCents: capital.OpeningCashCents, EntryBook: *b.PassiveBook, CutAt: p.CutAt}, a)
						if e != nil {
							return storage.PaperEvaluationPortfolio{}, e
						}
						t.Passive = &passive
					}
					returns = append(returns, t)
				}
			}
			row.Base, row.Stress = values[0], values[1]
			if benchmarks {
				row.BaseBenchmarks, row.StressBenchmarks = baselines[0], baselines[1]
			}
			if len(returns) == 2 {
				row.BaseTotalReturn, row.StressTotalReturn = returns[0], returns[1]
			}
			row.Status = "observed_book_equity_only_total_return_unavailable"
		}
		if decisions {
			row.Decisions, err = decisionTotalReturns(ctx, inputs, capital, v.StrategyID, actions)
			if err != nil {
				return storage.PaperEvaluationPortfolio{}, err
			}
		}
		out.Variants = append(out.Variants, row)
	}
	out.SHA256 = workerplan.Hash(out)
	return out, nil
}

// decisionTotalReturns evaluates each selected decision of a variant alone: a
// finite-capital book with the full opening cash that trades only that
// decision, through the same entry, protective-exit and total-return replay as
// the variant book. Decisions are reported only when the variant's entry
// replay ran, so every row has its market evidence.
func decisionTotalReturns(ctx context.Context, s storage.PaperEvaluationEntrySources, capital PaperEvaluationCapitalReplay, strategyID string, actions map[string]papereconomics.Actions) ([]storage.PaperEvaluationDecisionReturn, error) {
	ready := false
	for _, v := range capital.EntryEvidence.Variants {
		if v.StrategyID == strategyID && v.Base != nil && v.Stress != nil {
			ready = true
		}
	}
	if !ready {
		return nil, nil
	}
	out := []storage.PaperEvaluationDecisionReturn{}
	for i, r := range s.Rows {
		if r.Strategy.Command.Definition.ID != strategyID {
			continue
		}
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		market := capital.EntryEvidence.Rows[i].Market
		if market == nil || capital.EntryEvidence.Rows[i].DecisionID != r.Decision.Command.ID {
			return nil, storage.ErrPaperEvaluationAssessmentInvalid
		}
		d := storage.PaperEvaluationDecisionReturn{DecisionID: r.Decision.Command.ID, DecisionSHA256: r.Decision.SHA256}
		sources := []storage.PaperEvaluationEntrySource{r}
		commands, cut, _, err := protectiveReplayQuotes(ctx, sources, []PaperOutcomeMarketEvidence{*market})
		if err != nil {
			return nil, err
		}
		models := []*PaperEvaluationCapitalModel{}
		for _, stress := range []bool{false, true} {
			model := *s.Policy.Command.ExecutionModel
			if stress {
				model = s.Evidence.Declaration.Command.Plan.StressModel
			}
			m, err := replayCapitalModel(ctx, s, sources, commands, cut, model, stress)
			if errors.Is(err, paperfill.ErrCapacity) {
				d.Status = "capital_replay_capacity_exceeded"
				break
			}
			if err != nil {
				return nil, err
			}
			models = append(models, m)
		}
		a, covered := actions[r.Strategy.Command.Definition.Symbol]
		switch {
		case d.Status != "":
		case models[0].Replay == nil || models[1].Replay == nil:
			d.Status = "decision_not_funded"
		case !covered:
			d.Status = "instrument_not_covered"
		default:
			returns := []papereconomics.TotalReturn{}
			for _, m := range models {
				p := m.Replay
				t, err := papereconomics.EvaluateTotalReturn(ctx, papereconomics.Input{OpeningCashCents: capital.OpeningCashCents, EntryBook: p.EntryBook, ExitBook: p.ExitBook, CutAt: p.CutAt}, a)
				if err != nil {
					return nil, err
				}
				returns = append(returns, t)
			}
			d.BaseSHA256, d.StressSHA256 = returns[0].SHA256, returns[1].SHA256
			d.Status = "observed_total_return"
			for _, t := range returns {
				if !t.Available() {
					d.Status = t.Status
					break
				}
			}
			if d.Status == "observed_total_return" {
				d.BaseNetCents, d.StressNetCents = returns[0].NetCents, returns[1].NetCents
			}
		}
		out = append(out, d)
	}
	return out, nil
}
