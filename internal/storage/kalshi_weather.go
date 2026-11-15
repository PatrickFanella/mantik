package storage

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"time"

	"github.com/jackc/pgx/v5"
)

// KalshiWeatherPrediction binds immutable model evidence to a server admission
// clock. RecordedAt is assigned by PostgreSQL, never accepted from the caller.
type KalshiWeatherPrediction struct {
	ID, MarketTicker, ModelVersion string
	AsOf, CloseTime, RecordedAt    time.Time
	Probability                    float64
	Payload                        json.RawMessage
}

// KalshiWeatherSettlement retains a final venue result and original response.
type KalshiWeatherSettlement struct {
	MarketTicker          string
	Yes                   bool
	SettledAt, ObservedAt time.Time
	Original, SHA256      string
}

func validateWeatherPrediction(v KalshiWeatherPrediction) error {
	if !kalshiTicker.MatchString(v.MarketTicker) || v.ModelVersion == "" || len(v.ModelVersion) > 128 || !validKalshiHash(string(v.Payload), v.ID) || !json.Valid(v.Payload) || v.AsOf.IsZero() || !v.AsOf.Before(v.CloseTime) || math.IsNaN(v.Probability) || math.IsInf(v.Probability, 0) || v.Probability < 0 || v.Probability > 1 {
		return ErrInvalidKalshiRecord
	}

	var payload struct {
		MarketTicker, ModelVersion string
		AsOf, CloseTime            time.Time
		Probability                float64
		Evidence                   []struct {
			Original, SHA256 string
			ObservedAt       time.Time
		}
	}
	if json.Unmarshal(v.Payload, &payload) != nil || payload.MarketTicker != v.MarketTicker || payload.ModelVersion != v.ModelVersion || !payload.AsOf.Equal(v.AsOf) || !payload.CloseTime.Equal(v.CloseTime) || payload.Probability != v.Probability || len(payload.Evidence) == 0 {
		return ErrInvalidKalshiRecord
	}
	for _, e := range payload.Evidence {
		if !validKalshiHash(e.Original, e.SHA256) || e.ObservedAt.IsZero() || e.ObservedAt.After(v.AsOf) {
			return ErrInvalidKalshiRecord
		}
	}
	return nil
}

// RecordKalshiWeatherPrediction inserts once before close. An exact retry may
// retrieve its original receipt later; it cannot introduce a late prediction.
func (p *Postgres) RecordKalshiWeatherPrediction(ctx context.Context, v KalshiWeatherPrediction) (KalshiWeatherPrediction, error) {
	if err := validateWeatherPrediction(v); err != nil {
		return KalshiWeatherPrediction{}, err
	}
	tx, err := p.pool.Begin(ctx)
	if err != nil {
		return KalshiWeatherPrediction{}, err
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	if _, err = tx.Exec(ctx, "SELECT pg_advisory_xact_lock(hashtextextended('mantik_kalshi_weather',0))"); err != nil {
		return KalshiWeatherPrediction{}, err
	}
	var old KalshiWeatherPrediction
	err = tx.QueryRow(ctx, `SELECT id,market_ticker,model_version,as_of,close_time,recorded_at,probability,payload FROM mantik_kalshi_weather_predictions WHERE id=$1`, v.ID).Scan(&old.ID, &old.MarketTicker, &old.ModelVersion, &old.AsOf, &old.CloseTime, &old.RecordedAt, &old.Probability, &old.Payload)
	if err == nil {
		if old.MarketTicker != v.MarketTicker || old.ModelVersion != v.ModelVersion || !old.AsOf.Equal(v.AsOf) || !old.CloseTime.Equal(v.CloseTime) || old.Probability != v.Probability || string(old.Payload) != string(v.Payload) {
			return KalshiWeatherPrediction{}, ErrInvalidKalshiRecord
		}
		return old, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return KalshiWeatherPrediction{}, err
	}
	err = tx.QueryRow(ctx, `INSERT INTO mantik_kalshi_weather_predictions(id,market_ticker,model_version,as_of,close_time,probability,payload) VALUES($1,$2,$3,$4,$5,$6,$7) RETURNING recorded_at`, v.ID, v.MarketTicker, v.ModelVersion, v.AsOf, v.CloseTime, v.Probability, []byte(v.Payload)).Scan(&v.RecordedAt)
	if err != nil {
		return KalshiWeatherPrediction{}, fmt.Errorf("weather prediction admission: %w", err)
	}
	if err = tx.Commit(ctx); err != nil {
		return KalshiWeatherPrediction{}, err
	}
	return v, nil
}

// RecordKalshiWeatherSettlement admits one immutable final result per market.
func (p *Postgres) RecordKalshiWeatherSettlement(ctx context.Context, v KalshiWeatherSettlement) error {
	if !kalshiTicker.MatchString(v.MarketTicker) || !validKalshiHash(v.Original, v.SHA256) || v.SettledAt.IsZero() || v.ObservedAt.Before(v.SettledAt) {
		return ErrInvalidKalshiRecord
	}
	// Recheck normalized settlement against its raw venue response at storage.
	var wire struct {
		Market struct {
			Ticker, Status, Result string
			CloseTime              time.Time `json:"close_time"`
			SettledAt              time.Time `json:"settlement_ts"`
		} `json:"market"`
	}
	if json.Unmarshal([]byte(v.Original), &wire) != nil || wire.Market.Ticker != v.MarketTicker || wire.Market.Status != "finalized" || (wire.Market.Result != "yes" && wire.Market.Result != "no") || (wire.Market.Result == "yes") != v.Yes || !wire.Market.SettledAt.Equal(v.SettledAt) || wire.Market.CloseTime.IsZero() || v.SettledAt.Before(wire.Market.CloseTime) {
		return ErrInvalidKalshiRecord
	}
	raw, err := json.Marshal(v)
	if err != nil {
		return err
	}
	tx, err := p.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	if _, err = tx.Exec(ctx, "SELECT pg_advisory_xact_lock(hashtextextended('mantik_kalshi_weather',0))"); err != nil {
		return err
	}
	var existing []byte
	err = tx.QueryRow(ctx, "SELECT payload FROM mantik_kalshi_weather_settlements WHERE market_ticker=$1", v.MarketTicker).Scan(&existing)
	if err == nil {
		var old KalshiWeatherSettlement
		if json.Unmarshal(existing, &old) != nil || old.Yes != v.Yes || !old.SettledAt.Equal(v.SettledAt) {
			return ErrInvalidKalshiRecord
		}
		return nil // Later polling preserves the first final observation.
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	_, err = tx.Exec(ctx, `INSERT INTO mantik_kalshi_weather_settlements(market_ticker,observed_at,payload) SELECT $1,$2,$3 WHERE $2<=clock_timestamp() AND EXISTS (SELECT 1 FROM mantik_kalshi_weather_predictions WHERE market_ticker=$1 AND close_time<=$4)`, v.MarketTicker, v.ObservedAt, raw, v.SettledAt)
	if err != nil {
		return err
	}
	var exists bool
	if err = tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM mantik_kalshi_weather_settlements WHERE market_ticker=$1)", v.MarketTicker).Scan(&exists); err != nil {
		return err
	}
	if !exists {
		return ErrInvalidKalshiRecord
	}
	return tx.Commit(ctx)
}

// KalshiWeatherPredictions returns predictions actually recorded by the cutoff.
func (p *Postgres) KalshiWeatherPredictions(ctx context.Context, at time.Time) ([]KalshiWeatherPrediction, error) {
	if at.IsZero() {
		return nil, ErrInvalidKalshiRecord
	}
	rows, err := p.pool.Query(ctx, `SELECT id,market_ticker,model_version,as_of,close_time,recorded_at,probability,payload FROM mantik_kalshi_weather_predictions WHERE recorded_at<=$1 ORDER BY market_ticker,recorded_at,id`, at)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []KalshiWeatherPrediction{}
	for rows.Next() {
		var v KalshiWeatherPrediction
		if err = rows.Scan(&v.ID, &v.MarketTicker, &v.ModelVersion, &v.AsOf, &v.CloseTime, &v.RecordedAt, &v.Probability, &v.Payload); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

// KalshiWeatherSettlements exposes only final results observed by the cutoff.
func (p *Postgres) KalshiWeatherSettlements(ctx context.Context, at time.Time) ([]KalshiWeatherSettlement, error) {
	if at.IsZero() {
		return nil, ErrInvalidKalshiRecord
	}
	rows, err := p.pool.Query(ctx, "SELECT payload FROM mantik_kalshi_weather_settlements WHERE observed_at<=$1 AND recorded_at<=$1 ORDER BY market_ticker", at)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []KalshiWeatherSettlement{}
	for rows.Next() {
		var raw []byte
		var v KalshiWeatherSettlement
		if err = rows.Scan(&raw); err != nil {
			return nil, err
		}
		if err = json.Unmarshal(raw, &v); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}
