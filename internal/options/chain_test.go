package options

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"testing"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/alpacarecord"
	"git.subcult.tv/subculture-collective/mantik/internal/storage"
)

func fmtPrice(v float64) string { return strconv.FormatFloat(v, 'g', -1, 64) }
func chainFixture(t *testing.T) (*storage.Memory, Spot, time.Time, Assumptions, []alpacarecord.OptionRecord) {
	t.Helper()
	at := time.Date(2026, 1, 6, 22, 0, 0, 0, time.UTC)
	spot := Spot{AssetID: "asset-spy", Underlying: "SPY", Date: "2026-01-06", Price: 100, At: at.Add(-time.Hour), ObservedAt: at, AvailableAt: at}
	symbol := "SPY260116C00100000"
	r := alpacarecord.OptionRecord{Kind: "contract", Symbol: symbol, Underlying: "SPY", Key: "id", Feed: "reference", OccurredAt: at, ObservedAt: at, AvailableAt: at, Data: json.RawMessage(fmt.Sprintf(`{"id":"id","symbol":%q,"underlying_symbol":"SPY","expiration_date":"2026-01-16","strike_price":"100","type":"call","size":"100"}`, symbol))}
	b := r
	b.Kind = "bar"
	b.Key = spot.Date
	b.Feed = "indicative"
	b.FeedSelection = "provider_default_unverified"
	b.OccurredAt = time.Date(2026, 1, 6, 5, 0, 0, 0, time.UTC)
	b.Data = json.RawMessage(`{"c":2.0}`)
	m := storage.NewMemory()
	if _, err := m.RecordOptionData(context.Background(), []alpacarecord.OptionRecord{r, b}); err != nil {
		t.Fatal(err)
	}
	return m, spot, at, Assumptions{Rate: .03, Yield: .01, MaxAge: 4 * 24 * time.Hour}, []alpacarecord.OptionRecord{r, b}
}

func TestChainReadsOnlyKnownReferencesAndPrices(t *testing.T) {
	m, spot, at, a, records := chainFixture(t)
	ctx := context.Background()
	c, err := Read(ctx, m, spot, at, a)
	if err != nil {
		t.Fatal(err)
	}
	if len(c.Entries) != 1 || c.Feed != "indicative" || c.Model != ModelVersion || c.Entries[0].EstimateStatus != "ok" || c.Entries[0].IV <= 0 {
		t.Fatal(c)
	}
	if err := c.Validate(at, spot.AssetID); err != nil {
		t.Fatal(err)
	}
	// Corrections become visible only at their actual later acquisition time.
	correction := records[1]
	correction.AvailableAt = at.Add(time.Hour)
	correction.ObservedAt = correction.AvailableAt
	correction.Data = json.RawMessage(`{"c":3.0}`)
	if _, err := m.RecordOptionData(ctx, []alpacarecord.OptionRecord{correction}); err != nil {
		t.Fatal(err)
	}
	old, err := Read(ctx, m, spot, at, a)
	if err != nil || old.Entries[0].Close != 2 {
		t.Fatal(old, err)
	}
	newer, err := Read(ctx, m, spot, correction.AvailableAt, a)
	if err != nil || newer.Entries[0].Close != 3 {
		t.Fatal(newer, err)
	}
	// A reference acquired later does not retroactively populate a chain.
	empty := storage.NewMemory()
	if _, err := empty.RecordOptionData(ctx, []alpacarecord.OptionRecord{records[1]}); err != nil {
		t.Fatal(err)
	}
	c, err = Read(ctx, empty, spot, at, a)
	if err != nil || len(c.Entries) != 0 {
		t.Fatal(c, err)
	}
	c, err = Read(ctx, recordSource{records[:1]}, spot, at, a)
	if err != nil || len(c.Entries) != 1 || c.Entries[0].EstimateStatus != "missing_same_session_close" || c.Entries[0].IV != 0 {
		t.Fatal("missing close was not explicit", c, err)
	}
	// Retained entry clocks and prices can be inspected without implying that a
	// missing or invalid same-session price has a usable zero IV.
	for name, change := range map[string]func(*alpacarecord.OptionRecord){
		"different session": func(r *alpacarecord.OptionRecord) {
			r.Key = "2026-01-05"
			r.OccurredAt = r.OccurredAt.Add(-24 * time.Hour)
		},
		"outside bounds": func(r *alpacarecord.OptionRecord) { r.Data = json.RawMessage(`{"c":200}`) },
	} {
		t.Run(name, func(t *testing.T) {
			rs := append([]alpacarecord.OptionRecord(nil), records...)
			change(&rs[1])
			store := recordSource{rs}
			c, err := Read(ctx, store, spot, at, a)
			if err != nil || len(c.Entries) != 1 || c.Entries[0].EstimateStatus == "ok" || c.Entries[0].IV != 0 {
				t.Fatal(c, err)
			}
		})
	}
}

type recordSource struct{ records []alpacarecord.OptionRecord }

func (s recordSource) OptionChainAsOf(context.Context, string, time.Time) ([]alpacarecord.OptionRecord, error) {
	return s.records, nil
}

func TestChainRejectsLeakyOrInvalidSourceAndSpot(t *testing.T) {
	_, spot, at, a, records := chainFixture(t)
	ctx := context.Background()
	for name, change := range map[string]func(*alpacarecord.OptionRecord){
		"future price":     func(r *alpacarecord.OptionRecord) { r.AvailableAt = at.Add(time.Second) },
		"wrong underlying": func(r *alpacarecord.OptionRecord) { r.Underlying = "QQQ" },
		"wrong feed":       func(r *alpacarecord.OptionRecord) { r.Feed = "opra" },
		"bad data":         func(r *alpacarecord.OptionRecord) { r.Data = json.RawMessage(`{"c":0}`) },
	} {
		t.Run(name, func(t *testing.T) {
			rs := append([]alpacarecord.OptionRecord(nil), records...)
			change(&rs[1])
			if _, err := Read(ctx, recordSource{rs}, spot, at, a); err == nil {
				t.Fatal("invalid source admitted")
			}
		})
	}
	if _, err := Read(ctx, recordSource{append(records, records[0])}, spot, at, a); !errors.Is(err, ErrChain) {
		t.Fatal("duplicate", err)
	}
	for name, change := range map[string]func(*Spot){"future spot": func(s *Spot) { s.AvailableAt = at.Add(time.Second) }, "adjusted or zero spot": func(s *Spot) { s.Price = 0 }, "session mismatch": func(s *Spot) { s.Date = "2026-01-05" }, "no observation": func(s *Spot) { s.ObservedAt = time.Time{} }} {
		t.Run(name, func(t *testing.T) {
			s := spot
			change(&s)
			if _, err := Read(ctx, recordSource{records}, s, at, a); err == nil {
				t.Fatal("invalid spot admitted")
			}
		})
	}
	if _, err := Read(ctx, recordSource{records}, spot, at.Add(5*24*time.Hour), a); err == nil {
		t.Fatal("stale spot admitted")
	}
	// Expired and nonstandard deliverables are omitted without manufacturing a
	// price estimate. Same-day expiry follows the declared model clock.
	a.MaxAge = 20 * 24 * time.Hour
	c, err := Read(ctx, recordSource{records}, spot, at.Add(12*24*time.Hour), a)
	if err != nil || len(c.Entries) != 0 {
		t.Fatal(c, err)
	}
	adjusted := records[0]
	adjusted.Data = json.RawMessage(`{"id":"id","symbol":"SPY260116C00100000","underlying_symbol":"SPY","expiration_date":"2026-01-16","strike_price":"100","type":"call","size":"10"}`)
	c, err = Read(ctx, recordSource{[]alpacarecord.OptionRecord{adjusted}}, spot, at, a)
	if err != nil || len(c.Entries) != 0 {
		t.Fatal("nonstandard size admitted", c, err)
	}
}
