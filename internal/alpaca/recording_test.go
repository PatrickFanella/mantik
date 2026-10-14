package alpaca

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/domain"
	"git.subcult.tv/subculture-collective/mantik/internal/routing"
	"git.subcult.tv/subculture-collective/mantik/internal/storage"
)

var received = time.Date(2026, 10, 14, 15, 0, 1, 0, time.UTC)

func recording(raw string) Recording {
	return Recording{Schema: RecordingSchema, Currency: "USD", CaptureID: "synthetic-capture-v1", Capability: "latest_quotes", Feed: "iex", Symbols: []string{"SPY"}, Revision: 1, ReceivedAt: received, EvidenceClass: "synthetic_unqualified", AccessRef: "manufactured-fixture-v1", RawRef: "artifact:synthetic-response", Original: raw, SHA256: Digest(raw), MaxQuoteAgeSeconds: 60}
}

const goodQuotes = `{"quotes":{"SPY":{"t":"2026-10-14T15:00:00Z","ap":100.01,"bp":100.00,"as":10,"bs":12,"ax":"V","bx":"V"}}}`

func TestQuoteNormalizationRetainsProvenanceAndRoutesOnlyResearch(t *testing.T) {
	r := recording(goodQuotes)
	out, err := Normalize(r)
	if err != nil {
		t.Fatal(err)
	}
	again, err := Normalize(r)
	if err != nil || !reflect.DeepEqual(out, again) {
		t.Fatal("capture normalization changed on retry")
	}
	i := out.Inputs[0]
	if i.PriceCents != 10001 || i.Provenance.SHA256 != Digest(goodQuotes) || i.Source != "alpaca-iex" || i.Provenance.AvailableAt != received || out.Coverage[0].Status != "ready" {
		t.Fatal("price or provenance normalization lost")
	}
	store := storage.NewMemory()
	ctx := context.Background()
	d := domain.TriggerDefinition{ID: "alpaca-research", Owner: "operator", CaseKey: "alpaca-research", Behavior: "research", Condition: domain.Condition{Kind: "threshold", Comparison: "at_or_below", PriceCents: 10100}, ValidFrom: received.Add(-time.Minute), ValidUntil: received.Add(time.Hour), RepeatPolicy: "coalesce", Requirements: domain.SourceRequirements{Source: i.Source, Symbol: "SPY", EvidenceClass: r.EvidenceClass, MaxAgeSeconds: 60}}
	if _, err := store.RegisterTrigger(ctx, d); err != nil {
		t.Fatal(err)
	}
	receipt, err := store.RouteInput(ctx, i)
	if err != nil || len(receipt.Matches) != 1 {
		t.Fatal("normalized input failed research routing", err)
	}
	receipt, err = store.RouteInput(ctx, i)
	if err != nil || !receipt.Duplicate {
		t.Fatal("normalized retry repeated work")
	}
	snapshot, err := store.Snapshot(ctx)
	if err != nil || snapshot.ObservationCount != 0 || snapshot.State.Case.Entry != nil || len(snapshot.State.Actions.Items) != 0 {
		t.Fatal("Alpaca recording acquired execution")
	}
}

func TestQuoteCoverageFailuresDoNotInventPrices(t *testing.T) {
	tests := []struct{ name, raw, code string }{
		{"missing", `{"quotes":{}}`, "symbol_missing"},
		{"null", `{"quotes":{"SPY":null}}`, "symbol_missing"},
		{"subcent", strings.Replace(goodQuotes, "100.01", "100.001", 1), "unsupported_price_precision_or_range"},
		{"crossed", strings.Replace(goodQuotes, "100.00", "101.00", 1), "unusable_quote"},
		{"no_liquidity", strings.Replace(goodQuotes, `"as":10`, `"as":0`, 1), "unusable_quote"},
		{"future", strings.Replace(goodQuotes, "15:00:00Z", "15:01:00Z", 1), "invalid_quote"},
		{"stale", strings.Replace(goodQuotes, "15:00:00Z", "14:00:00Z", 1), "stale_quote"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			out, err := Normalize(recording(test.raw))
			if err != nil {
				t.Fatal(err)
			}
			i := out.Inputs[0]
			if i.Kind != "state" || i.PriceCents != 0 || i.Provenance.Quality == "usable" || out.Coverage[0].Code != test.code {
				t.Fatal("unusable evidence became a price", out)
			}
		})
	}
}

func TestCalendarSessionsDSTEarlyCloseAndRevision(t *testing.T) {
	raw := `[{"date":"2026-03-06","open":"09:30","close":"16:00"},{"date":"2026-03-09","open":"09:30","close":"13:00"}]`
	r := recording(raw)
	r.Capability, r.Feed, r.Start, r.End = "market_calendar", "regular_session", "2026-03-06", "2026-03-09"
	out, err := Normalize(r)
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Sessions) != 2 || out.Sessions[0].OpenAt.Hour() != 14 || out.Sessions[1].OpenAt.Hour() != 13 || out.Sessions[1].CloseAt.Hour() != 17 {
		t.Fatal("DST or early close lost", out.Sessions)
	}
	for _, i := range out.Inputs {
		if !i.OccurredAt.Equal(r.ReceivedAt) || i.EventName != "market_session_schedule" {
			t.Fatal("future session treated as an observed open/close")
		}
	}
	first := out.Inputs[0].ID
	r.Revision = 2
	revised, err := Normalize(r)
	if err != nil || revised.Inputs[0].ID == first {
		t.Fatal("calendar revision overwrote prior capture")
	}
	r.Original = `[]`
	r.SHA256 = Digest(r.Original)
	empty, err := Normalize(r)
	if err != nil || len(empty.Inputs) != 0 || len(empty.Sessions) != 0 {
		t.Fatal("empty calendar fabricated sessions")
	}
}

func TestRecordingAdmissionAndCalendarBounds(t *testing.T) {
	for _, name := range []string{"digest", "access", "reference", "currency", "symbols", "duplicate", "feed", "raw_shape", "unrequested", "revision", "class"} {
		t.Run(name, func(t *testing.T) {
			r := recording(goodQuotes)
			switch name {
			case "digest":
				r.SHA256 = strings.Repeat("a", 64)
			case "access":
				r.AccessRef = ""
			case "reference":
				r.RawRef = ""
			case "currency":
				r.Currency = "EUR"
			case "symbols":
				r.Symbols = nil
			case "duplicate":
				r.Symbols = []string{"SPY", "SPY"}
			case "feed":
				r.Feed = "auto"
			case "raw_shape":
				r.Original = `{"quote":{}}`
				r.SHA256 = Digest(r.Original)
			case "unrequested":
				r.Symbols = []string{"QQQ"}
			case "revision":
				r.Revision = 0
			case "class":
				r.EvidenceClass = "qualified"
			}
			if _, err := Normalize(r); err == nil {
				t.Fatal("invalid recording admitted")
			}
		})
	}
	for _, raw := range []string{`[{"date":"2026-03-10","open":"09:30","close":"16:00"}]`, `[{"date":"2026-03-06","open":"16:00","close":"09:30"}]`, `[{"date":"2026-03-06","open":"09:30","close":"16:00"},{"date":"2026-03-06","open":"09:30","close":"16:00"}]`} {
		r := recording(raw)
		r.Capability, r.Feed, r.Start, r.End = "market_calendar", "regular_session", "2026-03-06", "2026-03-09"
		if _, err := Normalize(r); err == nil {
			t.Fatal("invalid calendar admitted")
		}
	}
	r := recording(goodQuotes)
	raw, _ := json.Marshal(r)
	decoded, err := Read(strings.NewReader(string(raw)))
	if err != nil || !reflect.DeepEqual(r, decoded) {
		t.Fatal("recording read changed raw bytes")
	}
	if _, err := Read(strings.NewReader(string(raw) + "{}")); err == nil {
		t.Fatal("trailing capture admitted")
	}
	if routing.MarketInputID(domain.RoutingInput{}) != "" {
		t.Fatal("missing provenance acquired identity")
	}
}
