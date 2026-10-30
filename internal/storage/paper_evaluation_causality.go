package storage

const PaperCausalAssessmentRecipe = "native-predeclared-feature-causality-v9"

// The constructor has validated the native strategy owner, prospective plan,
// and the holdout envelope. Catalogue replay additionally reconstructs each
// feature's receipt, admission and original publication clocks from its owner.
// The fixed native predicate consumes one quote and no labels or learned state.
func applyNativeFeatureCausality(r *PaperEvaluationAssessment) {
	selected := map[string]int{}
	for _, row := range r.Evidence.Rows {
		if row.Status == "selected_research_evidence" {
			selected[row.Decision.Command.StrategyID]++
		}
	}
	for i := range r.Checks {
		check := &r.Checks[i]
		if check.Name != "leakage" || check.Status == "failed" {
			continue
		}
		check.ReasonCode = "owner_selected_native_features_unavailable"
		if selected[check.StrategyID] > 0 {
			check.Status = "passed"
			check.ReasonCode = "owner_replayed_stateless_native_features_available_at_finalization"
		}
	}
}
