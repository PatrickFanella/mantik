package kalshi

import (
	"context"
	"errors"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/storage"
)

// PaperCheckInput is sealed before settlement. It is a one-contract diagnostic
// taker exercise, never a maker fill, calibrated strategy or venue order.
// Original normalized/source objects and actual acquisition clocks survive
// restart; replay creates a fresh separate notional book and cannot double-pay.
type PaperCheckInput struct {
	Version    string
	Prediction storage.KalshiWeatherPrediction
	Markets    []storage.KalshiMarket
	Books      []storage.KalshiOrderBook
	Event      EventObservation
	Order      SimOrder
	ExecuteAt  time.Time
}

// PaperCheckReport joins the separate notional ledger to the original forecast
// and official settlement used by calibration. Missing settlement stays pending.
type PaperCheckReport struct {
	EvidenceClass       string
	QualificationCredit bool
	PredictionID        string
	Maker               MakerDecision
	Book                NotionalReport
	Calibration         WeatherCalibration
	Settlement          *storage.KalshiWeatherSettlement
}

type paperSources struct {
	SimulationStore
	weatherSnapshot
}

// CheckWeatherPaper replays sealed real or synthetic inputs with an explicit
// evidence class. Only the caller's retained source provenance can establish
// real observation; hashes and structural checks do not attest authenticity.
func CheckWeatherPaper(ctx context.Context, input PaperCheckInput, settlement *storage.KalshiWeatherSettlement, evidenceClass string) (PaperCheckReport, error) {
	out := PaperCheckReport{EvidenceClass: evidenceClass, PredictionID: input.Prediction.ID}
	if input.Version != "weather-paper-check-v1" || (evidenceClass != "prospective_market_unqualified" && evidenceClass != "synthetic_fixture") || input.Order.Contracts != 1 || input.Order.Action != "buy" || input.Order.MarketTicker != input.Prediction.MarketTicker || input.Order.At.Before(input.Prediction.RecordedAt) || !input.ExecuteAt.Before(input.Prediction.CloseTime) || len(input.Markets) > 200 || len(input.Books) > 1000 {
		return out, errors.New("invalid weather paper diagnostic")
	}
	if _, err := verifiedPrediction(input.Prediction); err != nil {
		return out, err
	}
	store := storage.NewMemory()
	if _, err := store.RecordKalshiData(ctx, input.Markets, input.Books); err != nil {
		return out, err
	}
	history := weatherSnapshot{Predictions: []storage.KalshiWeatherPrediction{input.Prediction}}
	source := paperSources{SimulationStore: store, weatherSnapshot: history}
	book, err := NewNotionalBook(100 * dollarMicros)
	if err != nil {
		return out, err
	}
	// The unchanged maker threshold must abstain with this empty calibration
	// history. The explicit diagnostic order below is not a maker fallback.
	out.Maker, err = book.PlaceWeatherMaker(ctx, source, "paper-check-maker", input.Order.MarketTicker, input.Prediction.ID, input.Order.At, input.Event, DefaultMakerPolicy())
	if err != nil {
		return out, err
	}
	if out.Maker.Intent != nil {
		return out, errors.New("diagnostic cannot replace a calibrated maker intent")
	}
	fills, err := book.Execute(ctx, store, input.Order, input.ExecuteAt, input.Event)
	if err != nil {
		return out, err
	}
	if len(fills) != 1 || fills[0].Contracts != 1 {
		return out, errors.New("diagnostic_requires_one_filled_contract")
	}
	at := input.ExecuteAt
	if settlement != nil {
		if settlement.MarketTicker != input.Order.MarketTicker || settlement.ObservedAt.Before(input.ExecuteAt) || settlement.SettledAt.Before(input.Prediction.CloseTime) {
			return out, errors.New("settlement differs from original paper forecast")
		}
		if err := validateOfficialSettlement(*settlement); err != nil {
			return out, err
		}
		history.Settlements = []storage.KalshiWeatherSettlement{*settlement}
		at = settlement.ObservedAt
		if err = book.Settle(ctx, history, input.Order.MarketTicker, at); err != nil {
			return out, err
		}
		copy := *settlement
		out.Settlement = &copy
	}
	out.Book = book.Report()
	out.Calibration, err = CalibrateWeather(ctx, history, input.Prediction.ModelVersion, at)
	return out, err
}
