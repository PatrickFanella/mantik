package service

import (
	"fmt"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/domain"
	"git.subcult.tv/subculture-collective/mantik/internal/storage"
	"git.subcult.tv/subculture-collective/mantik/internal/workerplan"
)

// DecisionExplanation is a deterministic rendering of an owner-verified,
// finalized research decision. It is not an admission or execution token.
type DecisionExplanation struct {
	Schema              string                      `json:"schema"`
	Method              string                      `json:"method"`
	ReceiverID          string                      `json:"receiver_id"`
	DecisionID          string                      `json:"decision_id"`
	DecisionSHA256      string                      `json:"decision_sha256"`
	BasisSHA256         string                      `json:"basis_sha256"`
	StrategyID          string                      `json:"strategy_id"`
	StrategySHA256      string                      `json:"strategy_sha256"`
	RegistrationID      string                      `json:"registration_id"`
	FinalizedAt         time.Time                   `json:"finalized_at"`
	Action              string                      `json:"action"`
	ReasonCode          string                      `json:"reason_code"`
	ReferencePriceCents int64                       `json:"reference_price_cents"`
	StoredSignal        bool                        `json:"stored_signal"`
	Coverage            domain.MarketCoverageReport `json:"coverage"`
	Statements          []string                    `json:"statements"`
	RecordPath          string                      `json:"record_path"`
	Authority           string                      `json:"authority"`
	ModelUsed           bool                        `json:"model_used"`
	AdmissionAvailable  bool                        `json:"admission_available"`
	ExecutionAvailable  bool                        `json:"execution_available"`
	QualificationCredit bool                        `json:"qualification_credit"`
	SHA256              string                      `json:"sha256"`
}

// The route obtains records through PaperDecisions, which replays their actual
// strategy/feed basis before calling this renderer. Envelope validation alone
// cannot establish custody of an imported record. No caller prose is rendered.
func explainPaperDecision(r storage.PaperDecisionRecord) (DecisionExplanation, error) {
	if r.Validate() != nil {
		return DecisionExplanation{}, storage.ErrPaperDecisionInvalid
	}
	e := r.Evaluation
	out := DecisionExplanation{Schema: "mantik-finalized-decision-explanation-v1", Method: "deterministic-retained-decision-facts-v1", ReceiverID: r.Command.ReceiverID, DecisionID: r.Command.ID, DecisionSHA256: r.SHA256, BasisSHA256: r.BasisSHA256, StrategyID: r.Command.StrategyID, StrategySHA256: r.Command.StrategySHA256, RegistrationID: r.Command.RegistrationID, FinalizedAt: r.FinalizedAt, Action: e.Action, ReasonCode: e.ReasonCode, ReferencePriceCents: e.PriceCents, StoredSignal: e.Signal, Coverage: e.Coverage, Statements: []string{}, RecordPath: "/v1/paper-decisions/" + r.Command.ID, Authority: "finalized_research_decision_explanation_only"}
	if e.Action == "entry_candidate" {
		out.Statements = append(out.Statements, fmt.Sprintf("Strategy %s finalized an entry candidate. Admission is still required.", out.StrategyID))
	} else {
		out.Statements = append(out.Statements, fmt.Sprintf("Strategy %s finalized an abstention.", out.StrategyID))
	}
	out.Statements = append(out.Statements, "Retained reason: "+e.ReasonCode+".")
	if e.PriceCents > 0 {
		out.Statements = append(out.Statements, fmt.Sprintf("The retained reference price was %d USD cents; this is not a fill price.", e.PriceCents))
	}
	if e.Coverage.Quote.Quality != "" {
		out.Statements = append(out.Statements, fmt.Sprintf("Retained quote coverage: %s (%s).", e.Coverage.Quote.Quality, e.Coverage.Quote.Code))
	}
	if e.Coverage.Session.Status != "" {
		out.Statements = append(out.Statements, fmt.Sprintf("Retained session status: %s (%s).", e.Coverage.Session.Status, e.Coverage.Session.Code))
	}
	out.Statements = append(out.Statements, "This explanation does not establish allocation, execution, realized profit, or strategy qualification.")
	out.SHA256 = workerplan.Hash(out)
	return out, nil
}
