package alpaca

import (
	"strings"
	"testing"
)

func TestInspectQuoteDimensions(t *testing.T) {
	r := recording(goodQuotes)
	normalized, err := Normalize(r)
	if err != nil {
		t.Fatal(err)
	}
	id := normalized.Inputs[0].ID
	d, err := InspectQuoteDimensions(r, id)
	if err != nil || d.InputID != id || d.AskCents != 10001 || d.BidCents != 10000 || d.ProviderAskSize != 10 || d.ProviderBidSize != 12 || d.SizeUnit != "provider_native_unverified" || d.ShareQuantitiesAvailable || d.CaptureSHA256 != r.SHA256 || d.EvidenceClass != r.EvidenceClass {
		t.Fatal(d, err)
	}
	for name, raw := range map[string]string{
		"missing":         `{"quotes":{}}`,
		"zero_size":       strings.Replace(goodQuotes, `"as":10`, `"as":0`, 1),
		"fractional_size": strings.Replace(goodQuotes, `"as":10`, `"as":0.5`, 1),
		"crossed":         strings.Replace(goodQuotes, `"bp":100.00`, `"bp":101.00`, 1),
		"subcent":         strings.Replace(goodQuotes, `"bp":100.00`, `"bp":100.001`, 1),
	} {
		t.Run(name, func(t *testing.T) {
			bad := recording(raw)
			if _, err := InspectQuoteDimensions(bad, id); err == nil {
				t.Fatal("unusable quote became dimensions")
			}
		})
	}
	if _, err := InspectQuoteDimensions(r, "other"); err == nil {
		t.Fatal("unknown input")
	}
	r.SHA256 = strings.Repeat("0", 64)
	if _, err := InspectQuoteDimensions(r, id); err == nil {
		t.Fatal("changed original digest")
	}
}
