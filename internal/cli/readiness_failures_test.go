package cli

import (
	"git.subcult.tv/subculture-collective/mantik/internal/storage"
	"testing"
)

func TestReadinessFailureShapeCannotGrantOrCrossVariants(t *testing.T) {
	c := storage.OwnedAdmissionReadiness{Schema: "mantik-owned-admission-readiness-v4", StrategyID: "one"}
	v := storage.OwnedAssessmentReadiness{SampleStatus: "pending", FailedChecks: []storage.PaperEvaluationCheck{{StrategyID: "one", Name: "leakage", Status: "failed", ReasonCode: "retained_candidate_source_unavailable_at_finalization"}}}
	if err := validateReadinessFailures(c, v); err != nil {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func(*storage.OwnedAssessmentReadiness){
		"other variant":          func(v *storage.OwnedAssessmentReadiness) { v.FailedChecks[0].StrategyID = "two" },
		"pass":                   func(v *storage.OwnedAssessmentReadiness) { v.FailedChecks[0].Status = "passed" },
		"unknown reason":         func(v *storage.OwnedAssessmentReadiness) { v.FailedChecks[0].ReasonCode = "caller_assertion" },
		"unknown check":          func(v *storage.OwnedAssessmentReadiness) { v.FailedChecks[0].Name = "profitability" },
		"duplicate":              func(v *storage.OwnedAssessmentReadiness) { v.FailedChecks = append(v.FailedChecks, v.FailedChecks[0]) },
		"missing sample failure": func(v *storage.OwnedAssessmentReadiness) { v.SampleStatus = "failed" },
	} {
		t.Run(name, func(t *testing.T) {
			changed := v
			changed.FailedChecks = append([]storage.PaperEvaluationCheck(nil), v.FailedChecks...)
			mutate(&changed)
			if validateReadinessFailures(c, changed) == nil {
				t.Fatal("malformed failure accepted", changed)
			}
		})
	}
	c.Schema = "mantik-owned-admission-readiness-v3"
	if validateReadinessFailures(c, v) == nil {
		t.Fatal("legacy acquired failure list")
	}
}
