package domain

import "time"

type Condition struct {
	Kind       string      `json:"kind"`
	EventName  string      `json:"event_name,omitempty"`
	StateKey   string      `json:"state_key,omitempty"`
	StateValue string      `json:"state_value,omitempty"`
	Comparison string      `json:"comparison,omitempty"`
	PriceCents int64       `json:"price_cents,omitempty"`
	All        []Condition `json:"all,omitempty"`
}

type SourceRequirements struct {
	Source        string `json:"source"`
	Symbol        string `json:"symbol"`
	EvidenceClass string `json:"evidence_class"`
	MaxAgeSeconds int64  `json:"max_age_seconds"`
}

type TriggerDefinition struct {
	ID                       string             `json:"id"`
	Owner                    string             `json:"owner"`
	CaseKey                  string             `json:"case_key"`
	Behavior                 string             `json:"behavior"`
	Condition                Condition          `json:"condition"`
	ValidFrom                time.Time          `json:"valid_from"`
	ValidUntil               time.Time          `json:"valid_until"`
	RepeatPolicy             string             `json:"repeat_policy"`
	CombinationWindowSeconds int64              `json:"combination_window_seconds,omitempty"`
	Requirements             SourceRequirements `json:"requirements"`
	SourceDecisionID         string             `json:"source_decision_id,omitempty"`
	SubjectCaseID            string             `json:"subject_case_id,omitempty"`
}

type RoutingInput struct {
	ID            string    `json:"id"`
	Kind          string    `json:"kind"`
	Source        string    `json:"source"`
	Symbol        string    `json:"symbol"`
	EvidenceClass string    `json:"evidence_class"`
	OccurredAt    time.Time `json:"occurred_at"`
	ReceivedAt    time.Time `json:"received_at"`
	PriceCents    int64     `json:"price_cents,omitempty"`
	EventName     string    `json:"event_name,omitempty"`
	StateKey      string    `json:"state_key,omitempty"`
	StateValue    string    `json:"state_value,omitempty"`
}

type ConditionFact struct {
	Satisfied      bool      `json:"satisfied"`
	InputRef       string    `json:"input_ref,omitempty"`
	At             time.Time `json:"at"`
	LastOccurredAt time.Time `json:"last_occurred_at"`
	LastReceivedAt time.Time `json:"last_received_at"`
	LastPrice      int64     `json:"last_price,omitempty"`
	LastState      string    `json:"last_state,omitempty"`
}

type TriggerRuntime struct {
	Status string                   `json:"status"`
	Fired  bool                     `json:"fired"`
	Facts  map[string]ConditionFact `json:"facts"`
}

type RoutingCase struct {
	ID               string    `json:"id"`
	Owner            string    `json:"owner"`
	CaseKey          string    `json:"case_key"`
	Behavior         string    `json:"behavior"`
	Generation       int       `json:"generation"`
	Status           string    `json:"status"`
	OpenedAt         time.Time `json:"opened_at"`
	UpdatedAt        time.Time `json:"updated_at"`
	SourceDecisionID string    `json:"source_decision_id,omitempty"`
	SubjectCaseID    string    `json:"subject_case_id,omitempty"`
	MatchCount       int64     `json:"match_count"`
	LastInputRef     string    `json:"last_input_ref"`
	LastDefinitionID string    `json:"last_definition_id"`
}

type RoutingMatch struct {
	DefinitionID string `json:"definition_id"`
	CaseID       string `json:"case_id"`
	Transition   string `json:"transition"`
}

type RoutingReceipt struct {
	InputID             string         `json:"input_id"`
	Duplicate           bool           `json:"duplicate"`
	Matches             []RoutingMatch `json:"matches"`
	ExpiredDefinitions  []string       `json:"expired_definitions"`
	RejectedDefinitions []string       `json:"rejected_definitions"`
}

type RetainedRoutingInput struct {
	Input   RoutingInput   `json:"input"`
	Receipt RoutingReceipt `json:"receipt"`
}

type RoutingState struct {
	Definitions []TriggerDefinition             `json:"definitions"`
	Runtime     map[string]TriggerRuntime       `json:"runtime"`
	Cases       []RoutingCase                   `json:"cases"`
	Inputs      map[string]RetainedRoutingInput `json:"inputs"`
}
