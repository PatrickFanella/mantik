package alpaca

import (
	"fmt"
	"math"
	"strings"
	"testing"
	"time"
)

func TestDocumentedQuoteShareDimensions(t *testing.T) {
	for _, tc := range []struct {
		name, feed, at string
		bid, ask       int64
		available      bool
		status         string
	}{
		{"iex", "iex", "2026-10-26T14:00:00Z", 7, 5, true, "documented_share_units"},
		{"sip", "sip", "2026-10-26T14:00:00Z", 7, 5, true, "documented_share_units"},
		{"delayed_sip", "delayed_sip", "2026-10-26T14:00:00Z", 7, 5, true, "documented_share_units"},
		{"legacy", "sip", "2025-10-31T14:00:00Z", 7, 5, false, "outside_documented_share_window"},
		{"transition_day", "sip", "2025-11-03T23:59:59Z", 7, 5, false, "outside_documented_share_window"},
		{"last_transition_ns", "iex", "2025-11-04T04:59:59.999999999Z", 7, 5, false, "outside_documented_share_window"},
		{"first_supported_ns", "iex", "2025-11-04T05:00:00Z", 7, 5, true, "documented_share_units"},
		{"uint32_boundary", "iex", "2026-10-26T14:00:00Z", math.MaxUint32, math.MaxUint32, true, "documented_share_units"},
		{"ask_overflow", "iex", "2026-10-26T14:00:00Z", 7, math.MaxUint32 + 1, false, "outside_documented_uint32_range"},
		{"bid_overflow", "iex", "2026-10-26T14:00:00Z", math.MaxUint32 + 1, 5, false, "outside_documented_uint32_range"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			at, err := time.Parse(time.RFC3339Nano, tc.at)
			if err != nil {
				t.Fatal(err)
			}
			r := recording(fmt.Sprintf(`{"quotes":{"SPY":{"t":%q,"ap":100.01,"bp":100.00,"as":%d,"bs":%d}}}`, tc.at, tc.ask, tc.bid))
			r.Feed, r.ReceivedAt = tc.feed, at.Add(time.Second)
			normalized, err := Normalize(r)
			if err != nil {
				t.Fatal(err)
			}
			id := normalized.Inputs[0].ID
			legacy, err := InspectQuoteDimensions(r, id)
			if err != nil || legacy.ShareQuantitiesAvailable || legacy.ShareSizeContract != nil || legacy.BidShares != 0 || legacy.AskShares != 0 {
				t.Fatal("legacy changed", legacy, err)
			}
			d, err := InspectQuoteShareDimensions(r, id)
			if err != nil || d.ShareQuantitiesAvailable != tc.available || d.ShareQuantityStatus != tc.status || d.ShareSizeContract == nil || d.ShareSizeContract.Method != QuoteShareSizeMethod || d.ShareSizeContract.SourceSHA256 != QuoteShareSizeSourceSHA256 || d.ShareSizeContract.Feed != tc.feed || d.ProviderBidSize != tc.bid || d.ProviderAskSize != tc.ask || d.CaptureSHA256 != r.SHA256 || d.EvidenceClass != r.EvidenceClass {
				t.Fatal(d, err)
			}
			if tc.available && (d.SizeUnit != "shares" || d.BidShares != tc.bid || d.AskShares != tc.ask) {
				t.Fatal("invented conversion", d)
			}
			if !tc.available && (d.BidShares != 0 || d.AskShares != 0 || d.SizeUnit != "provider_native_unverified") {
				t.Fatal("unsupported shares", d)
			}
			if err := ValidateQuoteShareDimensions(d); err != nil {
				t.Fatal(err)
			}
			d.AskShares++
			if ValidateQuoteShareDimensions(d) == nil {
				t.Fatal("changed shares accepted")
			}
			again, _ := InspectQuoteShareDimensions(r, id)
			again.ShareSizeContract.SourceSHA256 = strings.Repeat("0", 64)
			if ValidateQuoteShareDimensions(again) == nil {
				t.Fatal("substituted source pin accepted")
			}
		})
	}
}

func TestDocumentedQuoteShareDimensionsRejectUnusableSource(t *testing.T) {
	for _, raw := range []string{strings.Replace(goodQuotes, `"as":10`, `"as":0.5`, 1), strings.Replace(goodQuotes, `"as":10`, `"as":0`, 1), strings.Replace(goodQuotes, `"as":10`, `"as":9223372036854775808`, 1)} {
		r := recording(raw)
		normalized, err := Normalize(r)
		if err != nil || len(normalized.Inputs) != 1 {
			t.Fatal(normalized, err)
		}
		if _, err := InspectQuoteShareDimensions(r, normalized.Inputs[0].ID); err == nil {
			t.Fatal("unusable source accepted")
		}
	}
}
