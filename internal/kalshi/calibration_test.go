package kalshi

import (
	"context"
	"math"
	"reflect"
	"testing"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/storage"
)

func TestWeatherCalibrationKnownScoresAndCutoffs(t *testing.T) {
	at := time.Date(2026, 1, 3, 0, 0, 0, 0, time.UTC)
	prediction := func(id, ticker string, p float64) storage.KalshiWeatherPrediction {
		return storage.KalshiWeatherPrediction{ID: id, MarketTicker: ticker, ModelVersion: WeatherModelVersion, AsOf: at.Add(-48 * time.Hour), RecordedAt: at.Add(-47 * time.Hour), CloseTime: at.Add(-24 * time.Hour), Probability: p}
	}
	a := prediction("a", "A", 0.8)
	repeat := a
	repeat.ID = "repeat"
	repeat.Probability = 0.1
	repeat.RecordedAt = repeat.RecordedAt.Add(time.Hour)
	future := prediction("future", "F", 0.2)
	future.RecordedAt = at.Add(time.Hour)
	future.CloseTime = at.Add(24 * time.Hour)
	other := prediction("other", "O", 0.3)
	other.ModelVersion = "another-model"
	s := &weatherTestStore{predictions: []storage.KalshiWeatherPrediction{repeat, prediction("b", "B", 0.2), a, prediction("c", "C", 1), prediction("d", "D", 0), prediction("pending", "P", 0.5), future, other}}
	for _, ticker := range []string{"A", "B", "C", "D", "P"} {
		s.settlements = append(s.settlements, storage.KalshiWeatherSettlement{MarketTicker: ticker, Yes: ticker == "A" || ticker == "D", SettledAt: at.Add(-time.Hour), ObservedAt: at.Add(-30 * time.Minute)})
	}
	s.settlements[4].ObservedAt = at.Add(time.Hour) // Outcome unavailable at report cutoff.
	r, err := CalibrateWeather(context.Background(), s, WeatherModelVersion, at)
	if err != nil {
		t.Fatal(err)
	}
	if r.Predictions != 5 || r.Settled != 4 || r.Pending != 1 || r.BrierScore == nil || math.Abs(*r.BrierScore-0.52) > 1e-12 {
		t.Fatal(r)
	}
	if r.Buckets[8].Count != 1 || math.Abs(*r.Buckets[8].MeanProbability-0.8) > 1e-12 || *r.Buckets[8].ObservedFrequency != 1 || r.Buckets[9].Count != 1 || r.Buckets[0].Count != 1 || r.Buckets[5].BrierScore != nil {
		t.Fatal(r.Buckets)
	}
	before := append([]storage.KalshiWeatherPrediction(nil), s.predictions...)
	if _, err := CalibrateWeather(context.Background(), s, WeatherModelVersion, at.Add(2*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before, s.predictions) {
		t.Fatal("calibration changed predictions")
	}
	empty, err := CalibrateWeather(context.Background(), &weatherTestStore{}, WeatherModelVersion, at)
	if err != nil || empty.BrierScore != nil || empty.Settled != 0 || len(empty.Buckets) != 10 {
		t.Fatal(empty, err)
	}
}
