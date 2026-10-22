package storage

import (
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/allocation"
	"git.subcult.tv/subculture-collective/mantik/internal/paperstrategy"
	"git.subcult.tv/subculture-collective/mantik/internal/workerplan"
)

// These are prerequisite availability checks, not evaluation results or grants.
// Only the owner replay path constructs them from retained decisions and inputs.
type OwnedAdmissionGate struct {
	Name       string `json:"name"`
	Status     string `json:"status"`
	ReasonCode string `json:"reason_code"`
}

type OwnedAdmissionReadiness struct {
	Schema              string               `json:"schema"`
	ReceiverID          string               `json:"receiver_id"`
	DecisionID          string               `json:"decision_id"`
	DecisionSHA256      string               `json:"decision_sha256"`
	StrategyID          string               `json:"strategy_id"`
	StrategySHA256      string               `json:"strategy_sha256"`
	DefinitionSHA256    string               `json:"definition_sha256"`
	BasisSHA256         string               `json:"basis_sha256"`
	QuoteInputID        string               `json:"quote_input_id"`
	EvidenceClass       string               `json:"evidence_class"`
	CheckedAt           time.Time            `json:"checked_at"`
	ValidUntil          time.Time            `json:"valid_until"`
	Status              string               `json:"status"`
	Gates               []OwnedAdmissionGate `json:"gates"`
	ReconsiderTriggers  []string             `json:"reconsider_triggers"`
	Authority           string               `json:"authority"`
	AdmissionAvailable  bool                 `json:"admission_available"`
	ExecutionAvailable  bool                 `json:"execution_available"`
	QualificationCredit bool                 `json:"qualification_credit"`
	SHA256              string               `json:"sha256"`
}

// The comparison has already been resolved against immutable owner records.
// Missing qualification and execution owners deliberately leave admissions empty.
func ownedAdmissionReadiness(comparison allocation.Comparison, decisions PaperDecisionCatalog, strategies paperstrategy.Catalog, retained map[string]retainedPaperDecision, at time.Time) ([]OwnedAdmissionReadiness, error) {
	checks := make([]OwnedAdmissionReadiness, 0, len(comparison.Proposals))
	for _, proposal := range comparison.Proposals {
		basis, ok := retained[proposal.ID]
		if !ok || basis.Record.SHA256 != proposal.DecisionSHA256 || basis.Basis.Feed.Routing == nil {
			return nil, ErrOwnedAllocationConflict
		}
		decision := basis.Record
		strategy, err := paperstrategy.Select(strategies, decision.Command.StrategyID)
		if err != nil || strategy.SHA256 != decision.Command.StrategySHA256 {
			return nil, ErrOwnedAllocationConflict
		}
		quoteID := decision.Evaluation.Coverage.Quote.InputID
		input, ok := basis.Basis.Feed.Routing.Inputs[quoteID]
		if !ok || input.Input.EvidenceClass != strategy.Command.Definition.InputEvidenceClass {
			return nil, ErrOwnedAllocationConflict
		}
		class := input.Input.EvidenceClass
		switch class {
		case "synthetic_unqualified", "recorded_market_unqualified", "prospective_market_unqualified":
		default:
			return nil, ErrOwnedAllocationConflict
		}
		r := OwnedAdmissionReadiness{
			Schema:     "mantik-owned-admission-readiness-v1",
			ReceiverID: decisions.ReceiverID, DecisionID: proposal.ID, DecisionSHA256: decision.SHA256,
			StrategyID: strategy.Command.Definition.ID, StrategySHA256: strategy.SHA256, DefinitionSHA256: strategy.DefinitionSHA256,
			BasisSHA256: decision.BasisSHA256, QuoteInputID: quoteID, EvidenceClass: class,
			CheckedAt: at.UTC(), ValidUntil: proposal.ValidUntil, Status: "deferred",
			Gates: []OwnedAdmissionGate{
				{Name: "finalized_decision", Status: "verified", ReasonCode: "retained_owner_decision"},
				{Name: "source_evidence_class", Status: "unqualified", ReasonCode: class},
				{Name: "evaluation_recipe", Status: "unavailable", ReasonCode: "relevant_evaluation_receipt_unavailable"},
				{Name: "execution_reconciliation", Status: "unavailable", ReasonCode: "owned_execution_evidence_unavailable"},
			},
			ReconsiderTriggers: []string{"qualified_source_evidence", "applicable_evaluation_receipt", "owned_execution_evidence"},
			Authority:          "service_owned_admission_readiness_only",
		}
		if !at.Before(proposal.ValidUntil) {
			r.Status = "expired"
			r.ReconsiderTriggers = []string{"new_finalized_decision"}
		}
		r.SHA256 = workerplan.Hash(r)
		checks = append(checks, r)
	}
	return checks, nil
}
