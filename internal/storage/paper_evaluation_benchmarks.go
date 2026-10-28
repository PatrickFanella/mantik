package storage

import (
	"encoding/json"

	"git.subcult.tv/subculture-collective/mantik/internal/workerplan"
)

// V8 is selected only by the prospectively declared benchmark recipe. Older
// declarations and assessments retain their original accounting and hashes.
func paperAssessmentWithBenchmarks(c PaperEvaluationAssessmentCommand, e PaperEvaluationEvidence, w *PaperEvaluationWarmUp, cut *PaperEvaluationCorporateSourceCut, p *PaperEvaluationPortfolio, h *PaperEvaluationHoldout, schema string) (PaperEvaluationAssessment, error) {
	if schema != "mantik-owned-paper-evaluation-assessment-v8" {
		if e.Declaration.Command.Plan.BenchmarkMethod != "" {
			return PaperEvaluationAssessment{}, ErrPaperEvaluationAssessmentInvalid
		}
		return paperAssessmentWithHoldout(c, e, w, cut, p, h, schema)
	}
	if e.Declaration.Command.Recipe != PaperBenchmarkEvaluationRecipe || e.Declaration.Command.Plan.BenchmarkMethod == "" || p == nil || p.Method != PaperBenchmarkPortfolioMethod || h == nil {
		return PaperEvaluationAssessment{}, ErrPaperEvaluationAssessmentInvalid
	}
	r, err := paperAssessmentWithHoldout(c, e, w, cut, p, h, "mantik-owned-paper-evaluation-assessment-v7")
	if err != nil {
		return PaperEvaluationAssessment{}, err
	}
	r.Schema = schema
	r.Recipe = PaperBenchmarkAssessmentRecipe
	// Even a positive observed-price difference cannot pass the declared
	// total-return comparison without documented corporate coverage/reconciliation.
	for i, check := range r.Checks {
		if check.Name == "benchmark_comparison" && check.Status == "unavailable" {
			r.Checks[i].ReasonCode = "observed_price_benchmarks_do_not_establish_total_return_or_corporate_coverage"
		}
	}
	r.SHA256 = ""
	r.SHA256 = workerplan.Hash(r)
	raw, err := json.Marshal(r)
	if err != nil || len(raw) > MaxPaperEvaluationAssessmentBytes {
		return PaperEvaluationAssessment{}, ErrPaperEvaluationAssessmentCapacity
	}
	return r, nil
}
