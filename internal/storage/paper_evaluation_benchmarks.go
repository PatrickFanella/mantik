package storage

import (
	"encoding/json"

	"git.subcult.tv/subculture-collective/mantik/internal/workerplan"
)

// V8 is selected only by the prospectively declared benchmark recipe. Older
// declarations and assessments retain their original accounting and hashes.
func paperAssessmentWithBenchmarks(c PaperEvaluationAssessmentCommand, e PaperEvaluationEvidence, w *PaperEvaluationWarmUp, cut *PaperEvaluationCorporateSourceCut, p *PaperEvaluationPortfolio, h *PaperEvaluationHoldout, search *PaperEvaluationSearchHistory, coverage *PaperEvaluationCorporateCoverage, schema string) (PaperEvaluationAssessment, error) {
	statistical := schema == "mantik-owned-paper-evaluation-assessment-v15"
	sensitivity := schema == "mantik-owned-paper-evaluation-assessment-v14" || statistical
	totalReturn := schema == "mantik-owned-paper-evaluation-assessment-v13" || sensitivity
	covered := schema == "mantik-owned-paper-evaluation-assessment-v12" || totalReturn
	searched := schema == "mantik-owned-paper-evaluation-assessment-v11" || covered
	sampleDesign := schema == "mantik-owned-paper-evaluation-assessment-v10" || searched
	causal := schema == "mantik-owned-paper-evaluation-assessment-v9" || sampleDesign
	if !causal && schema != "mantik-owned-paper-evaluation-assessment-v8" {
		if e.Declaration.Command.Plan.BenchmarkMethod != "" {
			return PaperEvaluationAssessment{}, ErrPaperEvaluationAssessmentInvalid
		}
		if search != nil || coverage != nil {
			return PaperEvaluationAssessment{}, ErrPaperEvaluationAssessmentInvalid
		}
		return paperAssessmentWithHoldout(c, e, w, cut, p, h, schema)
	}
	expectedRecipe := PaperBenchmarkEvaluationRecipe
	if causal {
		expectedRecipe = PaperCausalEvaluationRecipe
	}
	if sampleDesign {
		expectedRecipe = PaperSampleDesignEvaluationRecipe
	}
	if searched {
		expectedRecipe = PaperSearchHistoryEvaluationRecipe
	}
	if covered {
		expectedRecipe = PaperCoverageEvaluationRecipe
	}
	expectedMethod := PaperBenchmarkPortfolioMethod
	if totalReturn {
		expectedRecipe, expectedMethod = PaperTotalReturnEvaluationRecipe, PaperTotalReturnPortfolioMethod
	}
	if sensitivity {
		expectedRecipe = PaperSensitivityEvaluationRecipe
	}
	if statistical {
		expectedRecipe, expectedMethod = PaperStatisticalEvaluationRecipe, PaperDecisionReturnPortfolioMethod
	}
	if statistical != (e.Declaration.Command.Plan.StatisticalTests != nil) || sensitivity != (e.Declaration.Command.Plan.Sensitivity != nil) || totalReturn != (e.Declaration.Command.Plan.TotalReturnMethod != "") || covered != (coverage != nil) || covered != (e.Declaration.Command.Plan.CorporateCoverage != nil) || searched != (search != nil) || searched != (e.Declaration.Command.Plan.SearchMultiplicity != nil) || sampleDesign != (e.Declaration.Command.Plan.SampleDesign != nil) || causal != (e.Declaration.Command.Plan.FeatureCausalityMethod != "") || e.Declaration.Command.Recipe != expectedRecipe || e.Declaration.Command.Plan.BenchmarkMethod == "" || p == nil || p.Method != expectedMethod || h == nil {
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
	if causal {
		r.Recipe = PaperCausalAssessmentRecipe
		applyNativeFeatureCausality(&r)
	}
	if sampleDesign {
		design, err := nativeSampleDesign(r)
		if err != nil {
			return PaperEvaluationAssessment{}, err
		}
		r.Recipe = PaperSampleDesignAssessmentRecipe
		applySampleDesign(&r, design)
	}
	if searched {
		history, err := retainedSearchHistory(e, search)
		if err != nil {
			return PaperEvaluationAssessment{}, err
		}
		r.Recipe = PaperSearchHistoryAssessmentRecipe
		applySearchHistory(&r, history)
	}
	if covered {
		value, err := retainedCorporateCoverage(e, coverage)
		if err != nil {
			return PaperEvaluationAssessment{}, err
		}
		r.Recipe = PaperCoverageAssessmentRecipe
		applyCorporateCoverage(&r, value)
	}
	if totalReturn {
		r.Recipe = PaperTotalReturnAssessmentRecipe
		if err := applyTotalReturnBenchmarks(&r, e.Declaration.Command.Plan.MinimumNetExcessCents); err != nil {
			return PaperEvaluationAssessment{}, err
		}
	}
	if sensitivity {
		r.Recipe = PaperSensitivityAssessmentRecipe
		if err := applyThresholdSensitivity(&r, e.Declaration.Command.Plan.Sensitivity); err != nil {
			return PaperEvaluationAssessment{}, err
		}
	}
	if statistical {
		r.Recipe = PaperStatisticalAssessmentRecipe
		if err := applyStatisticalTests(&r, e.Declaration.Command.Plan.StatisticalTests); err != nil {
			return PaperEvaluationAssessment{}, err
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
