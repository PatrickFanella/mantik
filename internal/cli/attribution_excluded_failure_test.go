package cli

import (
	"git.subcult.tv/subculture-collective/mantik/internal/storage"
	"git.subcult.tv/subculture-collective/mantik/internal/workerplan"
	"strings"
	"testing"
	"time"
)

// Portable validation preserves the owner semantics; this fixture is not a
// service-owned assessment or a source-custody attestation.
func TestAttributionExcludedAssessmentFailure(t *testing.T) {
	at := time.Date(2026, 10, 25, 1, 0, 0, 0, time.UTC)
	pin := strings.Repeat("a", 64)
	binding := storage.OwnedEvaluationPlanBinding{Assessments: []storage.OwnedEvaluationAssessmentPin{{ID: "assessment", SHA256: pin, DeclarationID: "declaration", DeclarationSHA256: pin}}}
	a := storage.PaperAllocationAttribution{EvaluationContext: &storage.PaperEvaluationPlanAttribution{Binding: binding}}
	c := storage.OwnedAdmissionReadiness{Schema: "mantik-owned-admission-readiness-v6", StrategyID: "strategy", CheckedAt: at, Binding: &storage.OwnedAdmissionBinding{EvaluationBindingSHA256: workerplan.Hash(binding)}, EvaluationEvidence: []storage.OwnedAssessmentReadiness{{AssessmentID: "assessment", AssessmentSHA256: pin, DeclarationID: "declaration", DeclarationSHA256: pin, EvidenceSHA256: pin, AssessedAt: at, DecisionEvidenceStatus: "source_unavailable_at_decision", SampleStatus: "pending", FailedChecks: []storage.PaperEvaluationCheck{{StrategyID: "strategy", Name: "leakage", Status: "failed", ReasonCode: "retained_candidate_source_unavailable_at_finalization"}}}}}
	gate, err := validateReadinessAssessmentSelection(c, a)
	if err != nil || gate.Status != "failed" || gate.ReasonCode != "retained_necessary_evaluation_check_failed" {
		t.Fatal(gate, err)
	}
	c.Schema = "mantik-owned-admission-readiness-v5"
	gate, err = validateReadinessAssessmentSelection(c, a)
	if err != nil || gate.Status != "unavailable" {
		t.Fatal("legacy semantics changed", gate, err)
	}
	c.Schema = "mantik-owned-admission-readiness-v6"
	for _, status := range []string{"decision_not_retained", "decision_before_window", "decision_after_window", "abstention", "quote_before_window", "quote_after_window", "synthetic_not_selected"} {
		c.EvaluationEvidence[0].DecisionEvidenceStatus = status
		gate, err = validateReadinessAssessmentSelection(c, a)
		if err != nil || gate.Status != "unavailable" {
			t.Fatal(status, gate, err)
		}
	}
	c.EvaluationEvidence[0].DecisionEvidenceStatus = "source_unavailable_at_decision"
	c.EvaluationEvidence[0].FailedChecks[0].StrategyID = "unrelated"
	if _, err = validateReadinessAssessmentSelection(c, a); err == nil {
		t.Fatal("foreign strategy failure accepted")
	}
}
