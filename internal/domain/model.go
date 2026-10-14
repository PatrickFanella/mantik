// Package domain defines the structured records exchanged by Mantik's workflows.
package domain

import "time"

// Money and prices use integer cents in this equity-like demo. Other instrument
// precisions require a versioned instrument contract before they are supported.
type Observation struct {
	ID         string    `json:"id"`
	Kind       string    `json:"kind"`
	Source     string    `json:"source"`
	Symbol     string    `json:"symbol"`
	OccurredAt time.Time `json:"occurred_at"`
	ReceivedAt time.Time `json:"received_at"`
	PriceCents int64     `json:"price_cents"`
}

type Trigger struct {
	ID            string `json:"id"`
	Kind          string `json:"kind"`
	Behavior      string `json:"behavior"`
	ObservationID string `json:"observation_id"`
}

type Hypothesis struct {
	ID             string `json:"id"`
	ClaimCode      string `json:"claim_code"`
	RefutationCode string `json:"refutation_code"`
	EvidenceClass  string `json:"evidence_class"`
}

type Safety struct {
	MaxNotionalCents   int64 `json:"max_notional_cents"`
	StopCents          int64 `json:"stop_cents"`
	TargetCents        int64 `json:"target_cents"`
	MaxHoldingSeconds  int64 `json:"max_holding_seconds"`
	MaxQuoteAgeSeconds int64 `json:"max_quote_age_seconds"`
}

type StrategyVersion struct {
	ID                  string     `json:"id"`
	Hypothesis          Hypothesis `json:"hypothesis"`
	Symbol              string     `json:"symbol"`
	EntryAtOrBelowCents int64      `json:"entry_at_or_below_cents"`
	Quantity            int64      `json:"quantity"`
	Safety              Safety     `json:"safety"`
}

type EvaluationResult struct {
	Check   string `json:"check"`
	Version string `json:"version,omitempty"`
	Passed  bool   `json:"passed"`
	Code    string `json:"code"`
}

type Decision struct {
	ID                string              `json:"id"`
	CaseID            string              `json:"case_id"`
	StrategyVersionID string              `json:"strategy_version_id"`
	ObservationID     string              `json:"observation_id"`
	Action            string              `json:"action"`
	ReasonCode        string              `json:"reason_code"`
	Status            string              `json:"status"`
	FinalizedAt       time.Time           `json:"finalized_at"`
	ValidUntil        time.Time           `json:"valid_until"`
	Quantity          int64               `json:"quantity"`
	Safety            Safety              `json:"safety"`
	Evaluations       []EvaluationResult  `json:"evaluations"`
	FutureConditions  []TriggerDefinition `json:"future_conditions,omitempty"`
}

type ExecutionOutcome struct {
	ID               string    `json:"id"`
	DecisionID       string    `json:"decision_id"`
	Mode             string    `json:"mode"`
	Action           string    `json:"action"`
	Quantity         int64     `json:"quantity"`
	PriceCents       int64     `json:"price_cents"`
	RealizedPnLCents int64     `json:"realized_pnl_cents"`
	FillModel        string    `json:"fill_model"`
	ExecutedAt       time.Time `json:"executed_at"`
}

type DecisionCase struct {
	ID                string            `json:"id"`
	StrategyVersionID string            `json:"strategy_version_id"`
	Status            string            `json:"status"`
	Entry             *ExecutionOutcome `json:"entry,omitempty"`
	Exit              *ExecutionOutcome `json:"exit,omitempty"`
	ExitBy            time.Time         `json:"exit_by,omitempty"`
}

// Event is an audit record, not narrative evaluation input.
type Event struct {
	ID            string            `json:"id"`
	Kind          string            `json:"kind"`
	ObservationID string            `json:"observation_id"`
	At            time.Time         `json:"at"`
	Trigger       *Trigger          `json:"trigger,omitempty"`
	Decision      *Decision         `json:"decision,omitempty"`
	Outcome       *ExecutionOutcome `json:"outcome,omitempty"`
}

type State struct {
	Strategy       StrategyVersion `json:"strategy"`
	Case           DecisionCase    `json:"case"`
	LastOccurredAt time.Time       `json:"last_occurred_at"`
	Routing        *RoutingState   `json:"routing"`
}

type Snapshot struct {
	Mode             string  `json:"mode"`
	ObservationCount int64   `json:"observation_count"`
	State            State   `json:"state"`
	Events           []Event `json:"events"`
}

type Receipt struct {
	ObservationID string  `json:"observation_id"`
	Duplicate     bool    `json:"duplicate"`
	Events        []Event `json:"events"`
}
