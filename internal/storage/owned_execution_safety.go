package storage

import (
	"git.subcult.tv/subculture-collective/mantik/internal/allocation"
	"git.subcult.tv/subculture-collective/mantik/internal/domain"
	"git.subcult.tv/subculture-collective/mantik/internal/paperstrategy"
	"git.subcult.tv/subculture-collective/mantik/internal/workerplan"
	"time"
)

// Requirements from actual owner records, never an order, admission, fill or
// permission to mutate capital. Holding time begins only with an actual future
// fill; no fill clock or holding deadline is manufactured here.
type OwnedExecutionSafety struct {
	Schema              string        `json:"schema"`
	ComparisonID        string        `json:"comparison_id"`
	DecisionID          string        `json:"decision_id"`
	DecisionSHA256      string        `json:"decision_sha256"`
	StrategySHA256      string        `json:"strategy_sha256"`
	DefinitionSHA256    string        `json:"definition_sha256"`
	BasisSHA256         string        `json:"basis_sha256"`
	PolicySHA256        string        `json:"policy_sha256"`
	AccountSHA256       string        `json:"account_sha256"`
	ModelSHA256         string        `json:"model_sha256"`
	CostCeilingSHA256   string        `json:"cost_ceiling_sha256"`
	QuoteInputID        string        `json:"quote_input_id"`
	FinalizedAt         time.Time     `json:"finalized_at"`
	ProposalValidUntil  time.Time     `json:"proposal_valid_until"`
	Quantity            int64         `json:"quantity"`
	EntryLimitCents     int64         `json:"entry_limit_cents"`
	Safety              domain.Safety `json:"safety"`
	Authority           string        `json:"authority"`
	AdmissionAvailable  bool          `json:"admission_available"`
	ExecutionAvailable  bool          `json:"execution_available"`
	QualificationCredit bool          `json:"qualification_credit"`
	SHA256              string        `json:"sha256"`
}

// Called only after the comparison and costs have been rederived from the same
// complete owner snapshot. A model-free policy has no execution requirements.
func ownedExecutionSafety(r OwnedAllocationRecord, policy allocation.PolicyRecord, strategies paperstrategy.Catalog, retained map[string]retainedPaperDecision) ([]OwnedExecutionSafety, error) {
	if policy.Command.ExecutionModel == nil {
		if len(r.ExecutionCosts) != 0 {
			return nil, ErrOwnedAllocationInvalid
		}
		return nil, nil
	}
	if r.Entry.Command.Comparison == nil || len(r.ExecutionCosts) != len(r.Entry.Command.Comparison.Proposals) {
		return nil, ErrOwnedAllocationInvalid
	}
	out := make([]OwnedExecutionSafety, 0, len(r.ExecutionCosts))
	for _, c := range r.ExecutionCosts {
		d, ok := retained[c.DecisionID]
		if !ok || d.Record.SHA256 != c.DecisionSHA256 {
			return nil, ErrOwnedAllocationConflict
		}
		s, err := paperstrategy.Select(strategies, d.Record.Command.StrategyID)
		if err != nil || s.SHA256 != d.Record.Command.StrategySHA256 || c.ModelSHA256 != workerplan.Hash(*policy.Command.ExecutionModel) || c.Quantity != s.Command.Definition.Quantity {
			return nil, ErrOwnedAllocationConflict
		}
		v := OwnedExecutionSafety{Schema: "mantik-owned-execution-safety-v1", ComparisonID: r.Command.ID, DecisionID: c.DecisionID, DecisionSHA256: c.DecisionSHA256, StrategySHA256: s.SHA256, DefinitionSHA256: s.DefinitionSHA256, BasisSHA256: d.Record.BasisSHA256, PolicySHA256: policy.SHA256, AccountSHA256: policy.Account.SHA256, ModelSHA256: c.ModelSHA256, CostCeilingSHA256: c.SHA256, QuoteInputID: c.QuoteInputID, FinalizedAt: d.Record.FinalizedAt, ProposalValidUntil: c.ValidUntil, Quantity: c.Quantity, EntryLimitCents: c.LimitCents, Safety: s.Command.Definition.Safety, Authority: "service_owned_execution_requirements_only"}
		v.SHA256 = workerplan.Hash(v)
		out = append(out, v)
	}
	return out, nil
}
