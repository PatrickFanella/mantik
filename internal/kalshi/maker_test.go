package kalshi

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/storage"
)

type makerStore struct {
	simStore
	*weatherTestStore
}

func makerFixture() (makerStore, time.Time, string, EventObservation) {
	s, at := simFixture()
	s.markets[0].ObservedAt = at.Add(-2 * time.Minute)
	s.books[0].ObservedAt = at.Add(-time.Minute)
	w := &weatherTestStore{}
	for i := 0; i < 20; i++ {
		ticker := fmt.Sprintf("OLD-%d", i)
		p := WeatherPrediction{MarketTicker: ticker, ModelVersion: "calibrated-test", Probability: .65, AsOf: at.Add(-48 * time.Hour), CloseTime: at.Add(-25 * time.Hour), Evidence: []WeatherEvidence{{Original: "past forecast", SHA256: simHash("past forecast"), ObservedAt: at.Add(-48 * time.Hour)}}}
		raw, _ := json.Marshal(p)
		w.predictions = append(w.predictions, storage.KalshiWeatherPrediction{ID: simHash(string(raw)), MarketTicker: ticker, ModelVersion: p.ModelVersion, Probability: p.Probability, AsOf: p.AsOf, CloseTime: p.CloseTime, RecordedAt: at.Add(-47 * time.Hour), Payload: raw})
		result := "no"
		if i < 16 {
			result = "yes"
		}
		settled := at.Add(-24 * time.Hour)
		raw, _ = json.Marshal(map[string]any{"market": map[string]any{"ticker": ticker, "status": "finalized", "result": result, "close_time": p.CloseTime, "settlement_ts": settled}})
		w.settlements = append(w.settlements, storage.KalshiWeatherSettlement{MarketTicker: ticker, Yes: i < 16, SettledAt: settled, ObservedAt: at.Add(-23 * time.Hour), Original: string(raw), SHA256: simHash(string(raw))})
	}

	p := WeatherPrediction{ModelVersion: "calibrated-test", MarketTicker: "KXTEST", Probability: .65, AsOf: at.Add(-time.Minute), CloseTime: at.Add(24 * time.Hour), Evidence: []WeatherEvidence{{Original: "forecast", SHA256: simHash("forecast"), ObservedAt: at.Add(-time.Minute)}}}
	raw, _ := json.Marshal(p)
	id := simHash(string(raw))
	w.predictions = append(w.predictions, storage.KalshiWeatherPrediction{ID: id, MarketTicker: p.MarketTicker, ModelVersion: p.ModelVersion, Probability: p.Probability, AsOf: p.AsOf, CloseTime: p.CloseTime, RecordedAt: at.Add(-time.Minute), Payload: raw})
	event := simEvent(at.Add(-3 * time.Minute))
	return makerStore{s, w}, at, id, event
}
func TestMakerRequiresCalibrationAndNeverExceedsQuarterKellyOrBook(t *testing.T) {
	s, at, id, event := makerFixture()
	b := newSim(t)
	policy := DefaultMakerPolicy()
	d, e := b.PlaceWeatherMaker(context.Background(), s, "quote", "KXTEST", id, at, event, policy)
	if e != nil || d.Intent == nil {
		t.Fatal(d, e)
	}
	q := d.Intent
	if q.FeeSourceSHA256 != s.markets[0].SeriesSHA256+"/"+event.SHA256 || q.MarketSHA256 != s.markets[0].SHA256 || q.CalibrationSHA256 == "" {
		t.Fatal("fee or calibration provenance missing", q)
	}
	if !q.PostOnly || q.Order.Limit != "0.560000" || q.Order.Side != "yes" || q.CalibrationSamples != 20 || q.ReservedMicros > int64(float64(b.cash)*q.QuarterKelly) || q.ReservedMicros > int64(float64(b.cash)*policy.MaximumBookFraction) {
		t.Fatal(q)
	}
	r := b.Report()
	if len(r.Fills) != 0 || len(r.MakerOrders) != 1 || r.ReservedMicros != q.ReservedMicros || r.IncludedInSPY {
		t.Fatal("resting order invented fill or entered SPY", r)
	}
	again, e := b.PlaceWeatherMaker(context.Background(), s, "quote", "KXTEST", id, at, event, policy)
	if e != nil || again.Intent.Order != q.Order || b.Report().ReservedMicros != r.ReservedMicros {
		t.Fatal("retry changed reservation", again, e)
	}
	d, e = b.PlaceWeatherMaker(context.Background(), s, "second", "KXTEST", id, at, event, policy)
	if e != nil {
		t.Fatal(e)
	}
	if d.Intent != nil && b.Report().ReservedMicros > int64(float64(b.cash)*policy.MaximumBookFraction) {
		t.Fatal("pending quotes exceeded aggregate cap")
	}
	if e = b.ExpireMakers(at.Add(policy.Lifetime)); e != nil || b.Report().ReservedMicros != 0 || len(b.Report().Fills) != 0 {
		t.Fatal("expiry manufactured a fill", b.Report(), e)
	}
	s.settlements = nil
	empty := newSim(t)
	d, e = empty.PlaceWeatherMaker(context.Background(), s, "uncalibrated", "KXTEST", id, at, event, policy)
	if e != nil || d.Intent != nil || d.Reason != "calibration_unavailable" {
		t.Fatal(d, e)
	}
}
func TestMakerRejectsUnknownAndFutureEvidence(t *testing.T) {
	for _, which := range []string{"prediction", "fees", "depth"} {
		t.Run(which, func(t *testing.T) {
			s, at, id, event := makerFixture()
			switch which {
			case "prediction":
				s.predictions[len(s.predictions)-1].RecordedAt = at.Add(time.Second)
			case "fees":
				event.ObservedAt = at.Add(time.Second)
			case "depth":
				s.books[0].ObservedAt = at.Add(time.Second)
			}
			if d, e := newSim(t).PlaceWeatherMaker(context.Background(), s, "q", "KXTEST", id, at, event, DefaultMakerPolicy()); e == nil && d.Intent != nil {
				t.Fatal("future evidence traded", d)
			}
		})
	}
}
func TestMakerInventoryAndTakerCannotReuseReservation(t *testing.T) {
	s, at, id, event := makerFixture()
	b := newSim(t)
	b.positions["OTHER/yes"] = 6
	d, e := b.PlaceWeatherMaker(context.Background(), s, "inventory", "KXTEST", id, at, event, DefaultMakerPolicy())
	if e != nil || d.Intent != nil {
		t.Fatal("held inventory ignored", d, e)
	}
	b = newSim(t)
	d, e = b.PlaceWeatherMaker(context.Background(), s, "maker", "KXTEST", id, at, event, DefaultMakerPolicy())
	if e != nil || d.Intent == nil {
		t.Fatal(d, e)
	}
	b.cash = d.Intent.ReservedMicros + 1
	s.books[0].ObservedAt = at.Add(time.Minute)
	if _, e = b.Execute(context.Background(), s, SimOrder{ID: "taker", MarketTicker: "KXTEST", Side: "yes", Action: "buy", Limit: "0.65", Contracts: 1, At: at}, at.Add(2*time.Minute), event); e == nil {
		t.Fatal("taker used maker-reserved cash")
	}
}

func TestMakerRejectsCorruptCalibrationSources(t *testing.T) {
	for _, kind := range []string{"prediction", "settlement"} {
		t.Run(kind, func(t *testing.T) {
			s, at, id, event := makerFixture()
			if kind == "prediction" {
				s.predictions[0].Probability = .9
			} else {
				s.settlements[0].Yes = !s.settlements[0].Yes
			}
			if _, e := newSim(t).PlaceWeatherMaker(context.Background(), s, "q", "KXTEST", id, at, event, DefaultMakerPolicy()); e == nil {
				t.Fatal("corrupted calibration evidence traded")
			}
		})
	}
}
