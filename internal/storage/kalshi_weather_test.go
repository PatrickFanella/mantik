package storage

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"math"
	"sync"
	"testing"
	"time"

	"git.subcult.tv/subculture-collective/mantik/migrations"
)

func weatherStorageFixture(t *testing.T, close time.Time) KalshiWeatherPrediction {
	t.Helper()
	at := time.Now().UTC().Add(-time.Second).Truncate(time.Microsecond)
	p := KalshiWeatherPrediction{MarketTicker: "KXHIGHCHI-TEST-T70", ModelVersion: "daily-high-normal-v1", AsOf: at, CloseTime: close, Probability: 0.75}
	raw, err := json.Marshal(struct {
		MarketTicker, ModelVersion string
		AsOf, CloseTime            time.Time
		Probability                float64
		Evidence                   []struct {
			Original, SHA256 string
			ObservedAt       time.Time
		}
	}{p.MarketTicker, p.ModelVersion, p.AsOf, p.CloseTime, p.Probability, []struct {
		Original, SHA256 string
		ObservedAt       time.Time
	}{{"{}", testWeatherHash([]byte("{}")), at}}})
	if err != nil {
		t.Fatal(err)
	}
	p.Payload = raw
	p.ID = testWeatherHash(raw)
	return p
}

func TestWeatherStorageValidation(t *testing.T) {
	valid := weatherStorageFixture(t, time.Now().UTC().Add(time.Hour).Truncate(time.Microsecond))
	if err := validateWeatherPrediction(valid); err != nil {
		t.Fatal(err)
	}
	for _, tt := range []struct {
		name   string
		change func(*KalshiWeatherPrediction)
	}{
		{"backdated close", func(p *KalshiWeatherPrediction) { p.CloseTime = p.AsOf }},
		{"altered probability", func(p *KalshiWeatherPrediction) { p.Probability = 0.1 }},
		{"nan", func(p *KalshiWeatherPrediction) { p.Probability = math.NaN() }},
		{"bad hash", func(p *KalshiWeatherPrediction) { p.ID = "invalid" }},
		{"future evidence", func(p *KalshiWeatherPrediction) {
			var v map[string]any
			if err := json.Unmarshal(p.Payload, &v); err != nil {
				t.Fatal(err)
			}
			v["Evidence"] = []map[string]any{{"Original": "{}", "SHA256": testWeatherHash([]byte("{}")), "ObservedAt": p.AsOf.Add(time.Second)}}
			raw, err := json.Marshal(v)
			if err != nil {
				t.Fatal(err)
			}
			p.Payload = raw
			p.ID = testWeatherHash(raw)
		}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			p := valid
			tt.change(&p)
			if err := validateWeatherPrediction(p); err == nil {
				t.Fatal("invalid prediction admitted")
			}
		})
	}
}

func TestPostgresContractsKalshiWeather(t *testing.T) {
	p := timerPostgres(t, "kalshi_weather")
	ctx := context.Background()
	deadline := time.Now().UTC().Add(time.Second).Truncate(time.Microsecond)
	prediction := weatherStorageFixture(t, deadline)
	stored, err := p.RecordKalshiWeatherPrediction(ctx, prediction)
	if err != nil {
		t.Fatal(err)
	}
	if stored.RecordedAt.IsZero() || !stored.RecordedAt.Before(deadline) {
		t.Fatal("not admitted before close", stored)
	}
	// Concurrent exact retries all retrieve the original admission clock.
	var wg sync.WaitGroup
	for range 2 {
		wg.Go(func() {
			got, err := p.RecordKalshiWeatherPrediction(ctx, prediction)
			if err != nil || !got.RecordedAt.Equal(stored.RecordedAt) {
				t.Error("retry", got, err)
			}
		})
	}
	wg.Wait()
	hidden, err := p.KalshiWeatherPredictions(ctx, stored.RecordedAt.Add(-time.Microsecond))
	if err != nil || len(hidden) != 0 {
		t.Fatal(hidden, err)
	}
	visible, err := p.KalshiWeatherPredictions(ctx, stored.RecordedAt)
	if err != nil || len(visible) != 1 || string(visible[0].Payload) != string(prediction.Payload) {
		t.Fatal(visible, err)
	}
	// Future-as-of backdating and a fake recorded_at cannot defeat the DB clock.
	future := weatherStorageFixture(t, time.Now().Add(2*time.Hour).UTC().Truncate(time.Microsecond))
	future.AsOf = time.Now().Add(time.Hour).UTC()
	if _, err := p.pool.Exec(ctx, `INSERT INTO mantik_kalshi_weather_predictions(id,market_ticker,model_version,as_of,close_time,recorded_at,probability,payload) VALUES('future','KX','model',$1,$2,$1,0.5,'{}')`, future.AsOf, future.CloseTime); err == nil {
		t.Fatal("future as-of accepted")
	}
	timer := time.NewTimer(time.Until(deadline) + time.Millisecond)
	defer timer.Stop()
	<-timer.C
	late := weatherStorageFixture(t, deadline)
	if _, err := p.RecordKalshiWeatherPrediction(ctx, late); err == nil {
		t.Fatal("late prediction admitted")
	}
	if retry, err := p.RecordKalshiWeatherPrediction(ctx, prediction); err != nil || !retry.RecordedAt.Equal(stored.RecordedAt) {
		t.Fatal("post-close exact retry", retry, err)
	}
	outcomeAt := time.Now().UTC().Truncate(time.Microsecond)
	source, err := json.Marshal(map[string]any{"market": map[string]any{"ticker": prediction.MarketTicker, "status": "finalized", "result": "yes", "close_time": deadline, "settlement_ts": outcomeAt}})
	if err != nil {
		t.Fatal(err)
	}
	settlement := KalshiWeatherSettlement{MarketTicker: prediction.MarketTicker, Yes: true, SettledAt: outcomeAt, ObservedAt: outcomeAt, Original: string(source), SHA256: testWeatherHash(source)}
	if err := p.RecordKalshiWeatherSettlement(ctx, settlement); err != nil {
		t.Fatal(err)
	}
	if err := p.RecordKalshiWeatherSettlement(ctx, settlement); err != nil {
		t.Fatal("outcome retry", err)
	}
	conflict := settlement
	conflict.Yes = false
	if err := p.RecordKalshiWeatherSettlement(ctx, conflict); err == nil {
		t.Fatal("outcome conflict admitted")
	}
	unknown := settlement
	unknown.MarketTicker = "UNKNOWN"
	if err := p.RecordKalshiWeatherSettlement(ctx, unknown); err == nil {
		t.Fatal("unknown outcome admitted")
	}
	outcomes, err := p.KalshiWeatherSettlements(ctx, outcomeAt.Add(-time.Microsecond))
	if err != nil || len(outcomes) != 0 {
		t.Fatal("future result visible", outcomes, err)
	}
	outcomes, err = p.KalshiWeatherSettlements(ctx, time.Now().UTC())
	if err != nil || len(outcomes) != 1 || !outcomes[0].Yes {
		t.Fatal(outcomes, err)
	}
	for range 2 {
		if _, err := p.pool.Exec(ctx, migrations.KalshiWeather); err != nil {
			t.Fatal("migration replay", err)
		}
	}
	for _, table := range []string{"mantik_kalshi_weather_predictions", "mantik_kalshi_weather_settlements"} {
		for _, sql := range []string{"UPDATE " + table + " SET market_ticker='mutated'", "DELETE FROM " + table, "TRUNCATE " + table} {
			if _, err := p.pool.Exec(ctx, sql); err == nil {
				t.Fatal("immutable table admitted", sql)
			}
		}
	}
}

func testWeatherHash(raw []byte) string { sum := sha256.Sum256(raw); return hex.EncodeToString(sum[:]) }
