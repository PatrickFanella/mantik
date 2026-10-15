package domain

// CaseCatalog is a current-state readback, separate from the audit event history.
type CaseCatalog struct {
	Mode    string        `json:"mode"`
	Fixture DecisionCase  `json:"fixture"`
	Routing []RoutingCase `json:"routing"`
}

type CaseInspection struct {
	Mode           string                `json:"mode"`
	Kind           string                `json:"kind"`
	Fixture        *DecisionCase         `json:"fixture,omitempty"`
	Strategy       *StrategyVersion      `json:"strategy,omitempty"`
	Routing        *RoutingCase          `json:"routing,omitempty"`
	LastDefinition *TriggerDefinition    `json:"last_definition,omitempty"`
	LastInput      *RetainedRoutingInput `json:"last_input,omitempty"`
	LastQuote      *Observation          `json:"last_quote,omitempty"`
	LastTimer      *TimerJob             `json:"last_timer,omitempty"`
	// References present in storage, not a claim of evidence quality or qualification.
	ReferencesAvailable bool `json:"references_available"`
}
