package storage

import (
	"slices"
	"strings"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/allocation"
	"git.subcult.tv/subculture-collective/mantik/internal/workerplan"
)

// This records what the actual assessment selected, not evaluation applicability
// or an admission. A shared strategy/policy alone cannot select a decision.
type OwnedAssessmentReadiness struct {
	FailedChecks           []PaperEvaluationCheck `json:"failed_checks,omitempty"`
	AssessmentID           string                 `json:"assessment_id"`
	AssessmentSHA256       string                 `json:"assessment_sha256"`
	DeclarationID          string                 `json:"declaration_id"`
	DeclarationSHA256      string                 `json:"declaration_sha256"`
	AssessedAt             time.Time              `json:"assessed_at"`
	EvidenceSHA256         string                 `json:"evidence_sha256"`
	DecisionEvidenceStatus string                 `json:"decision_evidence_status"`
	SampleStatus           string                 `json:"sample_status"`
}

// The catalogue was replayed once by the owner in the same lock/transaction.
// Choose the latest retained assessment per exact declaration at this cut;
// equal assessment clocks use the lexically greatest ID. Later cuts do not
// rewrite this record, and an older pending assessment cannot mask a later fail.
func bindOwnedAssessmentReadiness(r OwnedAllocationRecord, policy allocation.PolicyRecord, actual PaperEvaluationAssessmentCatalog) ([]OwnedAdmissionReadiness, error) {
	checks, err := bindOwnedAdmissionReadiness(r, policy)
	if err != nil || r.EvaluationContext == nil || actual.ReceiverID != r.Command.ReceiverID {
		return nil, ErrOwnedAllocationInvalid
	}
	for i := range checks {
		c := &checks[i]
		var binding *OwnedEvaluationPlanBinding
		for j := range r.EvaluationContext.Decisions {
			b := &r.EvaluationContext.Decisions[j]
			if b.DecisionID == c.DecisionID && b.DecisionSHA256 == c.DecisionSHA256 {
				binding = b
				break
			}
		}
		if binding == nil {
			return nil, ErrOwnedAllocationInvalid
		}
		latest := map[string]PaperEvaluationAssessment{}
		for _, pin := range binding.Assessments {
			found := false
			for _, a := range actual.Records {
				if a.Command.ID != pin.ID || a.SHA256 != pin.SHA256 || a.Command.DeclarationID != pin.DeclarationID || a.Command.DeclarationSHA256 != pin.DeclarationSHA256 || a.AssessedAt.After(r.At) {
					continue
				}
				found = true
				old, ok := latest[a.Command.DeclarationID]
				if !ok || a.AssessedAt.After(old.AssessedAt) || a.AssessedAt.Equal(old.AssessedAt) && a.Command.ID > old.Command.ID {
					latest[a.Command.DeclarationID] = a
				}
				break
			}
			if !found {
				return nil, ErrOwnedAllocationConflict
			}
		}
		c.Schema = "mantik-owned-admission-readiness-v3"
		if r.Schema == "mantik-owned-allocation-deferral-v10" || (r.Schema == "mantik-owned-allocation-deferral-v11" || (r.Schema == "mantik-owned-allocation-deferral-v12" || (r.Schema == "mantik-owned-allocation-deferral-v13" || r.Schema == "mantik-owned-allocation-deferral-v14"))) {
			c.Schema = "mantik-owned-admission-readiness-v4"
		}
		if r.Schema == "mantik-owned-allocation-deferral-v12" || (r.Schema == "mantik-owned-allocation-deferral-v13" || r.Schema == "mantik-owned-allocation-deferral-v14") {
			c.Schema = "mantik-owned-admission-readiness-v5"
		}
		if r.Schema == "mantik-owned-allocation-deferral-v14" {
			c.Schema = "mantik-owned-admission-readiness-v6"
		}
		c.Binding.EvaluationBindingSHA256 = workerplan.Hash(*binding)
		c.EvaluationEvidence = nil
		for _, a := range latest {
			v := OwnedAssessmentReadiness{AssessmentID: a.Command.ID, AssessmentSHA256: a.SHA256, DeclarationID: a.Command.DeclarationID, DeclarationSHA256: a.Command.DeclarationSHA256, AssessedAt: a.AssessedAt, EvidenceSHA256: a.Evidence.SHA256, DecisionEvidenceStatus: "decision_not_retained"}
			for _, row := range a.Evidence.Rows {
				if row.Decision.Command.ID == c.DecisionID && row.Decision.SHA256 == c.DecisionSHA256 {
					v.DecisionEvidenceStatus = row.Status
					break
				}
			}
			for _, check := range a.Checks {
				if check.StrategyID == c.StrategyID && check.Name == "sample_sufficiency" {
					v.SampleStatus = check.Status
				}
				if (r.Schema == "mantik-owned-allocation-deferral-v10" || (r.Schema == "mantik-owned-allocation-deferral-v11" || (r.Schema == "mantik-owned-allocation-deferral-v12" || (r.Schema == "mantik-owned-allocation-deferral-v13" || r.Schema == "mantik-owned-allocation-deferral-v14")))) && check.StrategyID == c.StrategyID && check.Status == "failed" {
					v.FailedChecks = append(v.FailedChecks, clone(check))
				}
			}
			// A v15 sign test can pass; readiness still grants nothing for it.
			if v.SampleStatus != "pending" && v.SampleStatus != "failed" && v.SampleStatus != "unavailable" && v.SampleStatus != "passed" {
				return nil, ErrOwnedAllocationInvalid
			}
			c.EvaluationEvidence = append(c.EvaluationEvidence, v)
		}
		slices.SortFunc(c.EvaluationEvidence, func(a, b OwnedAssessmentReadiness) int { return strings.Compare(a.DeclarationID, b.DeclarationID) })
		gate := OwnedAdmissionGate{Name: "evaluation_recipe", Status: "unavailable", ReasonCode: "relevant_evaluation_receipt_unavailable"}
		selected, pending, failed := false, false, false
		for _, v := range c.EvaluationEvidence {
			// This exact candidate's known source failure is evidence against
			// admission even though the recipe correctly excluded it from scoring.
			// Absent/out-of-window decisions still cannot inherit other failures.
			if c.Schema == "mantik-owned-admission-readiness-v6" && v.DecisionEvidenceStatus == "source_unavailable_at_decision" && len(v.FailedChecks) > 0 {
				failed = true
			}
			if v.DecisionEvidenceStatus != "selected_research_evidence" {
				continue
			}
			selected = true
			pending = pending || v.SampleStatus == "pending"
			failed = failed || v.SampleStatus == "failed" || len(v.FailedChecks) > 0
		}
		switch {
		case failed:
			gate.Status, gate.ReasonCode = "failed", "retained_necessary_sample_check_failed"
			if c.Schema == "mantik-owned-admission-readiness-v4" || (c.Schema == "mantik-owned-admission-readiness-v5" || c.Schema == "mantik-owned-admission-readiness-v6") {
				gate.ReasonCode = "retained_necessary_evaluation_check_failed"
			}
		case pending:
			gate.Status, gate.ReasonCode = "pending", "retained_evaluation_outcome_window_open"
		case selected:
			gate.ReasonCode = "retained_evaluation_recipe_incomplete"
		case len(c.EvaluationEvidence) > 0:
			gate.ReasonCode = "finalized_decision_not_selected_in_assessment"
		}
		for j := range c.Gates {
			if c.Gates[j].Name == gate.Name {
				c.Gates[j] = gate
			}
		}
		c.SHA256 = ""
		c.SHA256 = workerplan.Hash(*c)
	}
	return checks, nil
}
