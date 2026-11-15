package alpacarecord

import (
	"encoding/json"
	"time"
)

// OptionRecord retains one option reference or indicative market observation.
type OptionRecord struct {
	FeedSelection string          `json:"feed_selection,omitempty"`
	Kind          string          `json:"kind"`
	Symbol        string          `json:"symbol"`
	Underlying    string          `json:"underlying"`
	Key           string          `json:"key"`
	Feed          string          `json:"feed"`
	OccurredAt    time.Time       `json:"occurred_at"`
	ObservedAt    time.Time       `json:"observed_at"`
	AvailableAt   time.Time       `json:"available_at"`
	Data          json.RawMessage `json:"data"`
}

// OptionReceipt counts new observations and exact replayed observations.
type OptionReceipt struct {
	Inserted  int `json:"inserted"`
	Unchanged int `json:"unchanged"`
}
