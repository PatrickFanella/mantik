package domain

import "time"

// TimerState retains the scheduler clock, fenced jobs, and unresolved protection.
type TimerState struct {
	LastClock  time.Time                       `json:"last_clock"`
	Jobs       map[string]TimerJob             `json:"jobs"`
	Exceptions map[string]OperationalException `json:"exceptions"`
}

// TimerJob is one immutable definition's one-shot durable delivery.
type TimerJob struct {
	DefinitionID string        `json:"definition_id"`
	DueAt        time.Time     `json:"due_at"`
	Status       string        `json:"status"`
	Worker       string        `json:"worker,omitempty"`
	Generation   int64         `json:"generation"`
	ClaimedAt    time.Time     `json:"claimed_at"`
	LeaseUntil   time.Time     `json:"lease_until"`
	CompletedAt  time.Time     `json:"completed_at"`
	Receipt      *TimerReceipt `json:"receipt,omitempty"`
}

// TimerClaim identifies a particular lease generation, fencing crashed workers.
type TimerClaim struct {
	DefinitionID string `json:"definition_id"`
	Worker       string `json:"worker"`
	Generation   int64  `json:"generation"`
}

// TimerReceipt retains applicability and any protective decision or exception.
type TimerReceipt struct {
	DefinitionID string         `json:"definition_id"`
	Status       string         `json:"status"`
	At           time.Time      `json:"at"`
	Routing      RoutingReceipt `json:"routing"`
	Events       []Event        `json:"events"`
}

// OperationalException records protection that could not obtain usable evidence.
type OperationalException struct {
	ID               string            `json:"id"`
	CaseID           string            `json:"case_id"`
	DefinitionID     string            `json:"definition_id"`
	Code             string            `json:"code"`
	Capability       string            `json:"capability,omitempty"`
	Severity         string            `json:"severity,omitempty"`
	EvidenceRefs     []string          `json:"evidence_refs,omitempty"`
	ActionID         string            `json:"action_id,omitempty"`
	LastObservedAt   time.Time         `json:"last_observed_at,omitzero"`
	RecoveryAttempts []RecoveryAttempt `json:"recovery_attempts,omitempty"`
	TerminalOutcome  string            `json:"terminal_outcome,omitempty"`
	Status           string            `json:"status"`
	At               time.Time         `json:"at"`
	ResolvedAt       time.Time         `json:"resolved_at,omitzero"`
}
