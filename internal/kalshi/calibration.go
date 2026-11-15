package kalshi

import (
	"context"
	"errors"
	"math"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/storage"
)

// CalibrationBucket describes a fixed tenth-wide probability interval. The
// last interval includes 1. Empty buckets have null means, not fabricated zeros.
type CalibrationBucket struct {
	Lower, Upper                                   float64
	Count                                          int
	MeanProbability, ObservedFrequency, BrierScore *float64
}

// WeatherCalibration measures the earliest retained prediction per market for
// one model version, so repeated polling cannot overweight a settled market.
type WeatherCalibration struct {
	ModelVersion                  string
	AsOf                          time.Time
	Predictions, Settled, Pending int
	BrierScore                    *float64
	Buckets                       []CalibrationBucket
}

// CalibrateWeather is offline: it reads retained predictions and final results,
// without fetching forecasts, changing the model or contacting any provider.
func CalibrateWeather(ctx context.Context, store WeatherStore, version string, at time.Time) (WeatherCalibration, error) {
	if version == "" || at.IsZero() {
		return WeatherCalibration{}, errors.New("calibration requires model version and cutoff")
	}
	predictions, err := store.KalshiWeatherPredictions(ctx, at)
	if err != nil {
		return WeatherCalibration{}, err
	}
	settlements, err := store.KalshiWeatherSettlements(ctx, at)
	if err != nil {
		return WeatherCalibration{}, err
	}
	selected := map[string]storage.KalshiWeatherPrediction{}
	for _, p := range predictions {
		if p.ModelVersion != version || p.RecordedAt.After(at) {
			continue
		}
		if p.AsOf.After(p.RecordedAt) || !p.RecordedAt.Before(p.CloseTime) || !finite(p.Probability) || p.Probability < 0 || p.Probability > 1 {
			return WeatherCalibration{}, errors.New("invalid retained weather prediction")
		}
		old, ok := selected[p.MarketTicker]
		if !ok || p.RecordedAt.Before(old.RecordedAt) || p.RecordedAt.Equal(old.RecordedAt) && p.ID < old.ID {
			selected[p.MarketTicker] = p
		}
	}
	outcomes := map[string]storage.KalshiWeatherSettlement{}
	for _, s := range settlements {
		if !s.ObservedAt.After(at) && !s.SettledAt.After(s.ObservedAt) {
			outcomes[s.MarketTicker] = s
		}
	}
	r := WeatherCalibration{ModelVersion: version, AsOf: at, Predictions: len(selected), Buckets: make([]CalibrationBucket, 10)}
	var sumsP, sumsY, sumsB [10]float64
	// Sort-independent sums: predictions are visited in the store's stable order.
	for _, p := range predictions {
		chosen, ok := selected[p.MarketTicker]
		if !ok || p.ID != chosen.ID {
			continue
		}
		delete(selected, p.MarketTicker)
		s, ok := outcomes[p.MarketTicker]
		if !ok {
			r.Pending++
			continue
		}
		if s.SettledAt.Before(p.CloseTime) {
			return WeatherCalibration{}, errors.New("settlement precedes prediction close")
		}
		y := 0.0
		if s.Yes {
			y = 1
		}
		b := math.Pow(p.Probability-y, 2)
		i := min(int(p.Probability*10), 9)
		r.Buckets[i].Count++
		r.Settled++
		sumsP[i] += p.Probability
		sumsY[i] += y
		sumsB[i] += b
	}
	total := 0.0
	for i := range r.Buckets {
		b := &r.Buckets[i]
		b.Lower = float64(i) / 10
		b.Upper = float64(i+1) / 10
		if b.Count > 0 {
			n := float64(b.Count)
			b.MeanProbability = new(sumsP[i] / n)
			b.ObservedFrequency = new(sumsY[i] / n)
			b.BrierScore = new(sumsB[i] / n)
			total += sumsB[i]
		}
	}
	if r.Settled > 0 {
		r.BrierScore = new(total / float64(r.Settled))
	}
	return r, nil
}
