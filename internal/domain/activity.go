package domain

// ActivityPage is an append-order slice of the retained audit history. Through
// pins the upper boundary across requests; sequences need not be contiguous.
type ActivityPage struct {
	Mode      string          `json:"mode"`
	Through   int64           `json:"through"`
	NextAfter int64           `json:"next_after"`
	HasMore   bool            `json:"has_more"`
	Items     []ActivityEvent `json:"items"`
}

type ActivityEvent struct {
	Sequence int64 `json:"sequence"`
	Event    Event `json:"event"`
}
