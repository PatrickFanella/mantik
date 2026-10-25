package storage

// The evidence selector has replayed actual owner records and excluded source
// inputs unavailable at decision finalization. An exclusion is still a known
// failure of this necessary temporal condition for that predeclared variant;
// removing the row from scored samples must not erase the failure.
// Absence of such rows proves neither complete leakage control nor admission.
func applyKnownSourceLeakage(r *PaperEvaluationAssessment) {
	failed := map[string]bool{}
	for _, row := range r.Evidence.Rows {
		if row.Status == "source_unavailable_at_decision" {
			failed[row.Decision.Command.StrategyID] = true
		}
	}
	for i := range r.Checks {
		check := &r.Checks[i]
		if check.Name != "leakage" {
			continue
		}
		if failed[check.StrategyID] {
			check.Status = "failed"
			check.ReasonCode = "retained_candidate_source_unavailable_at_finalization"
			r.Status = "blocked"
		}
	}
}
