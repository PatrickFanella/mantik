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
	capital, err := inspectEvaluationCapitalReplay(ctx, inputs)
	if err != nil {
		return storage.PaperEvaluationPortfolio{}, err
	}
	out := storage.PaperEvaluationPortfolio{Method: storage.PaperPortfolioMethod, EvidenceSHA256: inputs.Evidence.SHA256, SourceSHA256: workerplan.Hash(inputs), PolicySHA256: inputs.Policy.SHA256, Variants: []storage.PaperEvaluationPortfolioVariant{}, Authority: "service_owned_observed_portfolio_accounting_only"}
	for _, v := range capital.Variants {
		row := storage.PaperEvaluationPortfolioVariant{StrategyID: v.StrategyID, Status: v.Status}
		if v.Base != nil && v.Stress != nil && v.Base.Replay != nil && v.Stress.Replay != nil {
			values := []*papereconomics.Result{}
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
			}
			row.Base, row.Stress = values[0], values[1]
			row.Status = "observed_book_equity_only_total_return_unavailable"
		}
		out.Variants = append(out.Variants, row)
	}
	out.SHA256 = workerplan.Hash(out)
	return out, nil
}
