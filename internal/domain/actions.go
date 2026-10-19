package domain

import "time"

// FixtureCapitalBinding pins the optional synthetic account owner. Its private
// replay checkpoint is stored separately from this public aggregate reference.
type FixtureCapitalBinding struct {
	ReceiverID    string `json:"receiver_id"`
	AccountID     string `json:"account_id"`
	AccountSHA256 string `json:"account_sha256"`
	ScopeCode     string `json:"scope_code"`
}

// ActionIntent is immutable finalized authority plus its original price evidence.
type ActionIntent struct {
	Version        string      `json:"version"`
	ID             string      `json:"id"`
	IdempotencyKey string      `json:"idempotency_key"`
	Decision       Decision    `json:"decision"`
	Quote          Observation `json:"quote"`
	Mode           string      `json:"mode"`
	TimerID        string      `json:"timer_id,omitempty"`
}

// Action retains delivery state separately from its immutable intent.
type Action struct {
	Intent     ActionIntent      `json:"intent"`
	Status     string            `json:"status"`
	Code       string            `json:"code,omitempty"`
	Worker     string            `json:"worker,omitempty"`
	Generation int64             `json:"generation"`
	Attempts   int64             `json:"attempts"`
	LeaseUntil time.Time         `json:"lease_until"`
	UpdatedAt  time.Time         `json:"updated_at"`
	Outcome    *ExecutionOutcome `json:"outcome,omitempty"`
}

// ActionState is the durable queue and its monotonic worker clock.
type ActionState struct {
	LastClock time.Time         `json:"last_clock"`
	Items     map[string]Action `json:"items"`
}

// ActionClaim fences one lease generation.
type ActionClaim struct {
	ID         string `json:"id"`
	Worker     string `json:"worker"`
	Generation int64  `json:"generation"`
}

// ActionEffect is the adapter's independently retained idempotent result.
type ActionEffect struct {
	Intent  ActionIntent      `json:"intent"`
	Status  string            `json:"status"`
	Outcome *ExecutionOutcome `json:"outcome,omitempty"`
	Code    string            `json:"code,omitempty"`
}
