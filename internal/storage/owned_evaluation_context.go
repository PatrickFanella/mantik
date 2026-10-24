package storage

import (
	"reflect"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/allocation"
	"git.subcult.tv/subculture-collective/mantik/internal/paperstrategy"
	"git.subcult.tv/subculture-collective/mantik/internal/workerplan"
)

// A declaration is a planned evaluation, never an evaluated result or grant.
// Only the allocation owner derives this context from its captured catalogue.
type OwnedAllocationEvaluationContext struct {
	AssessmentSourceSHA256 string                       `json:"assessment_source_sha256,omitempty"`
	Schema                 string                       `json:"schema"`
	SourceSHA256           string                       `json:"source_sha256"`
	Decisions              []OwnedEvaluationPlanBinding `json:"decisions"`
	Authority              string                       `json:"authority"`
	AdmissionAvailable     bool                         `json:"admission_available"`
	ExecutionAvailable     bool                         `json:"execution_available"`
	QualificationCredit    bool                         `json:"qualification_credit"`
}

type OwnedEvaluationPlanBinding struct {
	Assessments    []OwnedEvaluationAssessmentPin `json:"assessments,omitempty"`
	DecisionID     string                         `json:"decision_id"`
	DecisionSHA256 string                         `json:"decision_sha256"`
	StrategyID     string                         `json:"strategy_id"`
	StrategySHA256 string                         `json:"strategy_sha256"`
	PolicyID       string                         `json:"policy_id"`
	PolicySHA256   string                         `json:"policy_sha256"`
	Declarations   []OwnedDecisionPin             `json:"declarations"`
	Status         string                         `json:"status"`
}

func ownedEvaluationDeclarationSource(owner string, policies allocation.PolicyCatalog, strategies paperstrategy.Catalog, source PaperEvaluationDeclarationCatalog) (PaperEvaluationDeclarationCatalog, error) {
	if source.ReceiverID != owner || source.Records == nil {
		return PaperEvaluationDeclarationCatalog{}, ErrOwnedAllocationInvalid
	}
	all := map[string]PaperEvaluationDeclaration{}
	for _, d := range source.Records {
		if _, exists := all[d.Command.ID]; exists {
			return PaperEvaluationDeclarationCatalog{}, ErrOwnedAllocationInvalid
		}
		all[d.Command.ID] = d
	}
	validated, err := paperEvaluationDeclarationCatalog(owner, all, strategies, policies)
	if err != nil || !reflect.DeepEqual(validated, source) {
		return PaperEvaluationDeclarationCatalog{}, ErrOwnedAllocationInvalid
	}
	return clone(validated), nil
}

func validateOwnedEvaluationCapture(owner string, captured, current PaperEvaluationDeclarationCatalog, policies allocation.PolicyCatalog, strategies paperstrategy.Catalog, at time.Time) (PaperEvaluationDeclarationCatalog, error) {
	captured, err := ownedEvaluationDeclarationSource(owner, policies, strategies, captured)
	if err != nil {
		return PaperEvaluationDeclarationCatalog{}, err
	}
	for _, d := range captured.Records {
		found := false
		for _, actual := range current.Records {
			if reflect.DeepEqual(d, actual) {
				found = true
				break
			}
		}
		if !found || d.DeclaredAt.After(at) {
			return PaperEvaluationDeclarationCatalog{}, ErrOwnedAllocationConflict
		}
	}
	return captured, nil
}

func ownedAllocationEvaluationContext(r OwnedAllocationRecord, policy allocation.PolicyRecord, source PaperEvaluationDeclarationCatalog) *OwnedAllocationEvaluationContext {
	out := &OwnedAllocationEvaluationContext{Schema: "mantik-owned-allocation-evaluation-context-v1", SourceSHA256: workerplan.Hash(source), Decisions: []OwnedEvaluationPlanBinding{}, Authority: "service_owned_predeclarations_only"}
	for _, check := range r.AdmissionChecks {
		b := OwnedEvaluationPlanBinding{DecisionID: check.DecisionID, DecisionSHA256: check.DecisionSHA256, StrategyID: check.StrategyID, StrategySHA256: check.StrategySHA256, PolicyID: policy.Command.Policy.ID, PolicySHA256: policy.SHA256, Declarations: []OwnedDecisionPin{}, Status: "declaration_unavailable"}
		for _, d := range source.Records {
			if d.Command.PolicyID != b.PolicyID || d.Command.PolicySHA256 != b.PolicySHA256 {
				continue
			}
			for _, v := range d.Command.Plan.Variants {
				if v.ID == b.StrategyID && v.SHA256 == b.StrategySHA256 {
					b.Declarations = append(b.Declarations, OwnedDecisionPin{ID: d.Command.ID, SHA256: d.SHA256})
					b.Status = "declared_not_evaluated"
					break
				}
			}
		}
		out.Decisions = append(out.Decisions, b)
	}
	return out
}

// Pins carry the actual retained assessment identity and status. They are not
// sufficient evaluation results, applicability decisions, or admission grants.
type OwnedEvaluationAssessmentPin struct {
	ID                string `json:"id"`
	SHA256            string `json:"sha256"`
	DeclarationID     string `json:"declaration_id"`
	DeclarationSHA256 string `json:"declaration_sha256"`
	Status            string `json:"status"`
}

// This is internal owner input, resolved once under the same aggregate lock or
// transaction as comparison replay. Public allocation commands contain no such
// catalogue. The empty default preserves internal legacy replay fixtures.
func allocationAssessmentSource(owner string, sources []PaperEvaluationAssessmentCatalog) (PaperEvaluationAssessmentCatalog, error) {
	out := PaperEvaluationAssessmentCatalog{ReceiverID: owner, Records: []PaperEvaluationAssessment{}}
	if len(sources) > 1 {
		return out, ErrOwnedAllocationInvalid
	}
	if len(sources) == 0 {
		return out, nil
	}
	out = sources[0]
	if out.ReceiverID != owner || out.Records == nil {
		return PaperEvaluationAssessmentCatalog{}, ErrOwnedAllocationInvalid
	}
	return out, nil
}

func ownedAllocationAssessmentContext(r OwnedAllocationRecord, policy allocation.PolicyRecord, declarations PaperEvaluationDeclarationCatalog, assessments PaperEvaluationAssessmentCatalog) *OwnedAllocationEvaluationContext {
	out := ownedAllocationEvaluationContext(r, policy, declarations)
	out.Schema = "mantik-owned-allocation-evaluation-context-v2"
	out.Authority = "service_owned_incomplete_assessments_and_predeclarations_only"
	pins := struct {
		ReceiverID string             `json:"receiver_id"`
		Records    []OwnedDecisionPin `json:"records"`
	}{assessments.ReceiverID, []OwnedDecisionPin{}}
	for _, a := range assessments.Records {
		if a.AssessedAt.After(r.At) {
			continue
		}
		pins.Records = append(pins.Records, OwnedDecisionPin{ID: a.Command.ID, SHA256: a.SHA256})
		for i := range out.Decisions {
			b := &out.Decisions[i]
			for _, d := range b.Declarations {
				if d.ID == a.Command.DeclarationID && d.SHA256 == a.Command.DeclarationSHA256 {
					b.Assessments = append(b.Assessments, OwnedEvaluationAssessmentPin{ID: a.Command.ID, SHA256: a.SHA256, DeclarationID: d.ID, DeclarationSHA256: d.SHA256, Status: a.Status})
					b.Status = "assessed_not_admitted"
					break
				}
			}
		}
	}
	out.AssessmentSourceSHA256 = workerplan.Hash(pins)
	return out
}
