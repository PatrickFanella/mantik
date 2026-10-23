package alpaca

import (
	"encoding/json"
	"errors"
	"time"
)

// QuoteDimensions retains provider-native sizes. It deliberately has no share
// quantities or execution authority: the recording schema has no size-unit pin.
type QuoteDimensions struct {
	InputID                  string    `json:"input_id"`
	Symbol                   string    `json:"symbol"`
	Currency                 string    `json:"currency"`
	EvidenceClass            string    `json:"evidence_class"`
	CaptureSHA256            string    `json:"capture_sha256"`
	OccurredAt               time.Time `json:"occurred_at"`
	ReceivedAt               time.Time `json:"received_at"`
	BidCents                 int64     `json:"bid_cents"`
	AskCents                 int64     `json:"ask_cents"`
	ProviderBidSize          int64     `json:"provider_bid_size"`
	ProviderAskSize          int64     `json:"provider_ask_size"`
	SizeUnit                 string    `json:"size_unit"`
	ShareQuantitiesAvailable bool      `json:"share_quantities_available"`
}

// InspectQuoteDimensions rederives an exact usable input from original bytes.
// It performs no acquisition. The service must separately prove owner custody,
// publication, admission and as-of availability before using this inspection.
func InspectQuoteDimensions(r Recording, inputID string) (QuoteDimensions, error) {
	normalized, err := Normalize(r)
	if err != nil {
		return QuoteDimensions{}, err
	}
	if r.Capability != "latest_quotes" {
		return QuoteDimensions{}, errors.New("quote recording required")
	}
	var response struct {
		Quotes map[string]quote `json:"quotes"`
	}
	if json.Unmarshal([]byte(r.Original), &response) != nil {
		return QuoteDimensions{}, errors.New("invalid quote dimensions")
	}
	for _, input := range normalized.Inputs {
		if input.ID != inputID || input.Kind != "quote" || input.Provenance.Quality != "usable" {
			continue
		}
		q := response.Quotes[input.Symbol]
		bid, bidOK := cents(q.Bid)
		ask, askOK := cents(q.Ask)
		if !bidOK || !askOK || ask != input.PriceCents {
			return QuoteDimensions{}, errors.New("quote dimensions disagree with normalized input")
		}
		return QuoteDimensions{InputID: input.ID, Symbol: input.Symbol, Currency: r.Currency, EvidenceClass: input.EvidenceClass, CaptureSHA256: r.SHA256, OccurredAt: input.OccurredAt, ReceivedAt: input.ReceivedAt, BidCents: bid, AskCents: ask, ProviderBidSize: q.BidSize, ProviderAskSize: q.AskSize, SizeUnit: "provider_native_unverified"}, nil
	}
	return QuoteDimensions{}, errors.New("usable quote input not found")
}
