package alpaca

import (
	"errors"
	"math"
	"slices"
	"time"
)

const QuoteShareSizeMethod = "alpaca-stock-rest-share-sizes-2025-11-v1"
const QuoteShareSizeSourceSHA256 = "2cd9b000441f24075cc3110580c25747fabd7799caefb0d4dc3c7c69832a4eb0"

// This compiled interpretation is selected from retained recording scope, never
// supplied as an execution assertion. It establishes documented dimensions,
// not provider authenticity, available liquidity or an admission to operate.
type QuoteShareSizeContract struct {
	Method        string `json:"method"`
	APIPath       string `json:"api_path"`
	Feed          string `json:"feed"`
	SourceURL     string `json:"source_url"`
	SourceSHA256  string `json:"source_sha256"`
	SchemaPointer string `json:"schema_pointer"`
	EligibleFrom  string `json:"eligible_from"`
}

func quoteShareSizeContract(feed string) QuoteShareSizeContract {
	return QuoteShareSizeContract{
		Method: QuoteShareSizeMethod, APIPath: "/v2/stocks/quotes/latest", Feed: feed,
		SourceURL: "https://docs.alpaca.markets/us/openapi/market-data-api.json", SourceSHA256: QuoteShareSizeSourceSHA256,
		SchemaPointer: "/components/schemas/stock_quote/properties", EligibleFrom: "2025-11-04T05:00:00Z",
	}
}

// Exclude the entire 2025-11-03 New York transition day conservatively. The
// provider's dated schema establishes shares from November 3, but no intraday
// transition instant is assumed. Pre-transition lots need a separate contract.
func documentedQuoteShares(d QuoteDimensions, feed string) QuoteDimensions {
	contract := quoteShareSizeContract(feed)
	d.ShareSizeContract = &contract
	d.ShareQuantityStatus = "outside_documented_share_window"
	if d.OccurredAt.Before(time.Date(2025, 11, 4, 5, 0, 0, 0, time.UTC)) {
		return d
	}
	d.ShareQuantityStatus = "outside_documented_uint32_range"
	if d.ProviderBidSize < 1 || d.ProviderAskSize < 1 || d.ProviderBidSize > math.MaxUint32 || d.ProviderAskSize > math.MaxUint32 {
		return d
	}
	d.ShareQuantityStatus = "documented_share_units"
	d.SizeUnit = "shares"
	d.BidShares, d.AskShares = d.ProviderBidSize, d.ProviderAskSize
	d.ShareQuantitiesAvailable = true
	return d
}

// Retain the legacy raw inspector unchanged. New service-owned projections opt
// into this versioned interpretation after their actual storage custody replay.
func InspectQuoteShareDimensions(r Recording, inputID string) (QuoteDimensions, error) {
	d, err := InspectQuoteDimensions(r, inputID)
	if err != nil {
		return QuoteDimensions{}, err
	}
	return documentedQuoteShares(d, r.Feed), nil
}

// Checks readback consistency only; this cannot attest capture authenticity or
// replace source-owner replay. It issues no effect or admission.
func ValidateQuoteShareDimensions(d QuoteDimensions) error {
	if d.ShareSizeContract == nil || !slices.Contains([]string{"iex", "sip", "delayed_sip"}, d.ShareSizeContract.Feed) || d.OccurredAt.IsZero() || d.ReceivedAt.Before(d.OccurredAt) {
		return errors.New("invalid documented quote share dimensions")
	}
	base := d
	base.SizeUnit = "provider_native_unverified"
	base.ShareSizeContract = nil
	base.ShareQuantityStatus = ""
	base.BidShares, base.AskShares = 0, 0
	base.ShareQuantitiesAvailable = false
	want := documentedQuoteShares(base, d.ShareSizeContract.Feed)
	if *d.ShareSizeContract != *want.ShareSizeContract || d.ShareQuantityStatus != want.ShareQuantityStatus || d.SizeUnit != want.SizeUnit || d.BidShares != want.BidShares || d.AskShares != want.AskShares || d.ShareQuantitiesAvailable != want.ShareQuantitiesAvailable {
		return errors.New("documented quote share contract mismatch")
	}
	return nil
}
