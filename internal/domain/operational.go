package domain

import "time"

// OperationalState retains worker observations independently of liveness.
type OperationalState struct {
	LastClock time.Time               `json:"last_clock"`
	Workers   map[string]WorkerHealth `json:"workers"`
}

// WorkerHealth is the latest persisted cycle result for one capability.
type WorkerHealth struct {
	Capability     string    `json:"capability"`
	WorkerID       string    `json:"worker_id"`
	At             time.Time `json:"at"`
	LastSuccessAt  time.Time `json:"last_success_at"`
	IntervalMillis int64     `json:"interval_millis"`
	EvidenceRef    string    `json:"evidence_ref,omitempty"`
	// EvidenceRefs names what a failed cycle failed on, up to a bound.
	// FailureCount counts all of them, including any past the bound.
	EvidenceRefs []string `json:"evidence_refs,omitempty"`
	FailureCount int      `json:"failure_count,omitempty"`
	ErrorCode    string   `json:"error_code,omitempty"`
	Stopped      bool     `json:"stopped"`
}

// WorkerReport includes a locally retained failure if storage was unavailable.
type WorkerReport struct {
	WorkerHealth
	PriorFailureAt time.Time `json:"prior_failure_at,omitzero"`
	PriorErrorCode string    `json:"prior_error_code,omitempty"`
}

// RecoveryAttempt retains one immutable operator request and its bounded result.
type RecoveryAttempt struct {
	RequestID string    `json:"request_id"`
	Operation string    `json:"operation"`
	At        time.Time `json:"at"`
	Outcome   string    `json:"outcome"`
	ActionID  string    `json:"action_id,omitempty"`
}

// RecoveryReceipt reports an attempt without treating it as execution success.
type RecoveryReceipt struct {
	ExceptionID string          `json:"exception_id"`
	Attempt     RecoveryAttempt `json:"attempt"`
	Duplicate   bool            `json:"duplicate"`
}

// CapabilityReadiness separates availability from evidence qualification.
type CapabilityReadiness struct {
	Ready        bool     `json:"ready"`
	Status       string   `json:"status"`
	Code         string   `json:"code"`
	EvidenceRefs []string `json:"evidence_refs"`
}

// ReadinessReport makes its requested scope explicit even when other scopes fail.
type ReadinessReport struct {
	Scope        string                         `json:"scope"`
	Ready        bool                           `json:"ready"`
	Status       string                         `json:"status"`
	At           time.Time                      `json:"at"`
	Mode         string                         `json:"mode"`
	Capabilities map[string]CapabilityReadiness `json:"capabilities"`
}

// OperationsSnapshot exposes persisted evidence together with current readiness.
type OperationsSnapshot struct {
	Readiness  ReadinessReport         `json:"readiness"`
	Workers    map[string]WorkerHealth `json:"workers"`
	Exceptions []OperationalException  `json:"exceptions"`
}
