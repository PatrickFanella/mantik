package service

import (
	"context"

	"git.subcult.tv/subculture-collective/mantik/internal/papereconomics"
	"git.subcult.tv/subculture-collective/mantik/internal/storage"
	"git.subcult.tv/subculture-collective/mantik/internal/workerplan"
)

// EvaluateOwnedPaperPortfolio is the compiled constructor dependency for the
// assessment owner. The owner supplies its replayed transaction snapshot; no
// public command can provide books, calculations, balances or evaluation flags.
func EvaluateOwnedPaperPortfolio(ctx context.Context, inputs storage.PaperEvaluationEntrySources) (storage.PaperEvaluationPortfolio, error) {
	if inputs.AccountingRecipe != "" && inputs.AccountingRecipe != storage.PaperBenchmarkAssessmentRecipe && inputs.AccountingRecipe != storage.PaperTotalReturnAssessmentRecipe {
		return storage.PaperEvaluationPortfolio{}, storage.ErrPaperEvaluationAssessmentInvalid
	}
	totalReturn := inputs.AccountingRecipe == storage.PaperTotalReturnAssessmentRecipe
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
	capital, err := inspectEvaluationCapitalReplay(ctx, inputs)
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
		out.Variants = append(out.Variants, row)
	}
	out.SHA256 = workerplan.Hash(out)
	return out, nil
}
