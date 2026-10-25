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
	EvaluationEvidence  []OwnedAssessmentReadiness `json:"evaluation_evidence,omitempty"`
	Binding             *OwnedAdmissionBinding     `json:"binding,omitempty"`
	Schema              string                     `json:"schema"`
	ReceiverID          string                     `json:"receiver_id"`
	DecisionID          string                     `json:"decision_id"`
	DecisionSHA256      string                     `json:"decision_sha256"`
	StrategyID          string                     `json:"strategy_id"`
	StrategySHA256      string                     `json:"strategy_sha256"`
	DefinitionSHA256    string                     `json:"definition_sha256"`
	BasisSHA256         string                     `json:"basis_sha256"`
	QuoteInputID        string                     `json:"quote_input_id"`
	EvidenceClass       string                     `json:"evidence_class"`
	CheckedAt           time.Time                  `json:"checked_at"`
	ValidUntil          time.Time                  `json:"valid_until"`
	Status              string                     `json:"status"`
	Gates               []OwnedAdmissionGate       `json:"gates"`
	ReconsiderTriggers  []string                   `json:"reconsider_triggers"`
	Authority           string                     `json:"authority"`
	AdmissionAvailable  bool                       `json:"admission_available"`
	ExecutionAvailable  bool                       `json:"execution_available"`
	QualificationCredit bool                       `json:"qualification_credit"`
	SHA256              string                     `json:"sha256"`
}

// OwnedAdmissionBinding identifies the owner-selected comparison context. It is
// not an admission or an effect command; the bound readiness remains deferred.
type OwnedAdmissionBinding struct {
	ExecutionSafetySHA256   string `json:"execution_safety_sha256,omitempty"`
	EvaluationBindingSHA256 string `json:"evaluation_binding_sha256,omitempty"`
	ComparisonID            string `json:"comparison_id"`
	PolicyID                string `json:"policy_id"`
	PolicySHA256            string `json:"policy_sha256"`
	AccountID               string `json:"account_id"`
	AccountSHA256           string `json:"account_sha256"`
	ProposalSHA256          string `json:"proposal_sha256"`
	LedgerSHA256            string `json:"ledger_sha256"`
	ExecutionCostSHA256     string `json:"execution_cost_sha256,omitempty"`
}

// The caller is the owner append/replay path after it has derived the actual
// comparison, costs and ledger entry. No binding is accepted over the public API.
func bindOwnedAdmissionReadiness(r OwnedAllocationRecord, policy allocation.PolicyRecord) ([]OwnedAdmissionReadiness, error) {
	if policy.Validate() != nil || r.PolicySHA256 != policy.SHA256 || r.Entry.Command.Comparison == nil || !decisionPin.MatchString(r.LedgerSHA256) || r.Command.ID != r.Entry.Command.ID || len(r.AdmissionChecks) != len(r.Entry.Command.Comparison.Proposals) {
		return nil, ErrOwnedAllocationInvalid
	}
	proposals := map[string]allocation.Proposal{}
	for _, p := range r.Entry.Command.Comparison.Proposals {
		if _, exists := proposals[p.ID]; exists {
			return nil, ErrOwnedAllocationInvalid
		}
		proposals[p.ID] = p
	}
	costs := map[string]OwnedExecutionCost{}
	for _, c := range r.ExecutionCosts {
		p, ok := proposals[c.DecisionID]
		if _, exists := costs[c.DecisionID]; exists || !ok || c.DecisionSHA256 != p.DecisionSHA256 {
			return nil, ErrOwnedAllocationInvalid
		}
		costs[c.DecisionID] = c
	}
	if policy.Command.ExecutionModel != nil && len(costs) != len(proposals) || policy.Command.ExecutionModel == nil && len(costs) != 0 {
		return nil, ErrOwnedAllocationInvalid
	}
	safety := map[string]OwnedExecutionSafety{}
	if r.Schema == "mantik-owned-allocation-deferral-v12" || r.Schema == "mantik-owned-allocation-deferral-v13" {
		for _, v := range r.ExecutionSafety {
			cost, ok := costs[v.DecisionID]
			if _, duplicate := safety[v.DecisionID]; duplicate || !ok || v.DecisionSHA256 != cost.DecisionSHA256 || v.CostCeilingSHA256 != cost.SHA256 || v.AccountSHA256 != policy.Account.SHA256 || v.PolicySHA256 != policy.SHA256 {
				return nil, ErrOwnedAllocationInvalid
			}
			safety[v.DecisionID] = v
		}
		if len(safety) != len(costs) {
			return nil, ErrOwnedAllocationInvalid
		}
	}
	checks := clone(r.AdmissionChecks)
	seen := map[string]bool{}
	for i := range checks {
		c := &checks[i]
		p, ok := proposals[c.DecisionID]
		if !ok || seen[c.DecisionID] || p.DecisionSHA256 != c.DecisionSHA256 || c.ReceiverID != policy.Command.ReceiverID || c.StrategyID != p.StrategyID || !c.ValidUntil.Equal(p.ValidUntil) {
			return nil, ErrOwnedAllocationInvalid
		}
		seen[c.DecisionID] = true
		c.Schema = "mantik-owned-admission-readiness-v2"
		c.EvaluationEvidence = nil
		c.Binding = &OwnedAdmissionBinding{ComparisonID: r.Command.ID, PolicyID: policy.Command.Policy.ID, PolicySHA256: policy.SHA256, AccountID: policy.Account.Config.ID, AccountSHA256: policy.Account.SHA256, ProposalSHA256: workerplan.Hash(p), LedgerSHA256: r.LedgerSHA256}
		if cost, ok := costs[c.DecisionID]; ok {
			c.Binding.ExecutionCostSHA256 = cost.SHA256
		}
		if v, ok := safety[c.DecisionID]; ok {
			c.Binding.ExecutionSafetySHA256 = v.SHA256
		}
		c.SHA256 = ""
		c.SHA256 = workerplan.Hash(*c)
	}
	return checks, nil
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
