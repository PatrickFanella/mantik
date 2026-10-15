package domain

// ExceptionPage contains persisted records, not newly derived readiness faults.
type ExceptionPage struct {
	Mode      string                 `json:"mode"`
	Scope     string                 `json:"scope"`
	NextAfter string                 `json:"next_after"`
	HasMore   bool                   `json:"has_more"`
	Items     []OperationalException `json:"items"`
}
