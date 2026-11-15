package kalshi

import (
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"time"
)

// EventObservation is a retained public event response and its acquisition
// clock. It is required for simulation because event fees may override series.
// FetchSimulationEvent captures the raw response in the private journal first.
type EventObservation struct {
	Ticker, Original, SHA256 string
	ObservedAt               time.Time
}

// FetchSimulationEvent reads public event metadata; it has no order capability.
func (c *Client) FetchSimulationEvent(ctx context.Context, ticker string) (EventObservation, error) {
	if !validTicker(ticker) || c.journal == nil {
		return EventObservation{}, errors.New("simulation event requires valid ticker and raw journal")
	}
	raw, at, err := c.fetch(ctx, "/events/"+url.PathEscape(ticker))
	if err != nil {
		return EventObservation{}, err
	}
	var wire struct {
		Event struct {
			Ticker string `json:"event_ticker"`
		} `json:"event"`
	}
	if json.Unmarshal(raw, &wire) != nil || wire.Event.Ticker != ticker {
		return EventObservation{}, errors.New("invalid event response")
	}
	return EventObservation{ticker, string(raw), digest(raw), at}, nil
}
