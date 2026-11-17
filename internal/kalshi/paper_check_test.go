package kalshi

import (
	"context"
	"encoding/json"
	"math"
	"testing"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/storage"
)

func paperFixture(t *testing.T) PaperCheckInput {
	t.Helper()
	s, at := simFixture()
	s.markets[0].ResolutionSource = "fixture official source"
	s.markets[0].ResolutionURL = "https://example.test/official"
	p := WeatherPrediction{ModelVersion: WeatherModelVersion, MarketTicker: "KXTEST", Probability: .65, AsOf: at.Add(-time.Minute), CloseTime: at.Add(24 * time.Hour), Evidence: []WeatherEvidence{{Original: "forecast", SHA256: simHash("forecast"), ObservedAt: at.Add(-time.Minute)}}}
	raw, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	return PaperCheckInput{Version: "weather-paper-check-v1", Prediction: storage.KalshiWeatherPrediction{ID: simHash(string(raw)), MarketTicker: p.MarketTicker, ModelVersion: p.ModelVersion, Probability: p.Probability, AsOf: p.AsOf, CloseTime: p.CloseTime, RecordedAt: at.Add(-time.Second), Payload: raw}, Markets: s.markets, Books: s.books, Event: simEvent(at.Add(-2 * time.Minute)), Order: SimOrder{ID: "one", MarketTicker: "KXTEST", Side: "yes", Action: "buy", Contracts: 1, Limit: "0.65", At: at}, ExecuteAt: at.Add(2 * time.Minute)}
}

func TestWeatherPaperPendingThenOfficialSettlementFeedsCalibration(t *testing.T) {
	for _, yes := range []bool{false, true} {
		t.Run(map[bool]string{false: "no", true: "yes"}[yes], func(t *testing.T) {
			input := paperFixture(t)
			pending, err := CheckWeatherPaper(context.Background(), input, nil, "synthetic_fixture")
			if err != nil {
				t.Fatal(err)
			}
			if pending.Maker.Reason != "calibration_unavailable" || pending.Maker.Intent != nil || pending.Book.IncludedInSPY || pending.QualificationCredit || pending.Calibration.Pending != 1 || pending.Calibration.BrierScore != nil || pending.Book.Positions["KXTEST/yes"] != 1 {
				t.Fatal(pending)
			}
			r := officialResult(input.Order.At.Add(25*time.Hour), yes)
			settled, err := CheckWeatherPaper(context.Background(), input, &r, "synthetic_fixture")
			if err != nil {
				t.Fatal(err)
			}
			payout := int64(0)
			wantBrier := .65 * .65
			if yes {
				payout = dollarMicros
				wantBrier = .35 * .35
			}
			if settled.Book.CashMicros != pending.Book.CashMicros+payout || settled.Book.PayoutMicros != payout || len(settled.Book.Positions) != 0 || settled.Calibration.Settled != 1 || settled.Calibration.Pending != 0 || settled.Calibration.BrierScore == nil || math.Abs(*settled.Calibration.BrierScore-wantBrier) > 1e-12 {
				t.Fatal(settled)
			}
			first, err := json.Marshal(settled)
			if err != nil {
				t.Fatal(err)
			}
			again, err := CheckWeatherPaper(context.Background(), input, &r, "synthetic_fixture")
			if err != nil {
				t.Fatal(err)
			}
			second, err := json.Marshal(again)
			if err != nil {
				t.Fatal(err)
			}
			if string(first) != string(second) {
				t.Fatal("restart duplicated settlement or changed calibration")
			}
		})
	}
}

func TestWeatherPaperRejectsLateForecastTamperingAndWrongOutcomes(t *testing.T) {
	for name, change := range map[string]func(*PaperCheckInput){
		"late forecast":             func(p *PaperCheckInput) { p.Prediction.RecordedAt = p.Prediction.CloseTime },
		"decision before admission": func(p *PaperCheckInput) { p.Order.At = p.Prediction.RecordedAt.Add(-time.Second) },
		"quantity":                  func(p *PaperCheckInput) { p.Order.Contracts = 2 },
		"payload":                   func(p *PaperCheckInput) { p.Prediction.Payload = append(p.Prediction.Payload, ' ') },
		"no later book":             func(p *PaperCheckInput) { p.Books[0].ObservedAt = p.Order.At },
		"fee source":                func(p *PaperCheckInput) { p.Event.SHA256 = "bad" },
	} {
		t.Run(name, func(t *testing.T) {
			p := paperFixture(t)
			change(&p)
			if _, err := CheckWeatherPaper(context.Background(), p, nil, "synthetic_fixture"); err == nil {
				t.Fatal("invalid paper evidence accepted")
			}
		})
	}
	p := paperFixture(t)
	r := officialResult(p.Order.At.Add(25*time.Hour), true)
	r.Yes = false
	if _, err := CheckWeatherPaper(context.Background(), p, &r, "synthetic_fixture"); err == nil {
		t.Fatal("changed official result accepted")
	}
	if _, err := CheckWeatherPaper(context.Background(), p, nil, "qualified"); err == nil {
		t.Fatal("invented qualification class")
	}
}

func TestWeatherPaperRequiresTheDiagnosticContractToFill(t *testing.T) {
	for _, kind := range []string{"uncrossed_limit", "empty_depth"} {
		t.Run(kind, func(t *testing.T) {
			input := paperFixture(t)
			if kind == "uncrossed_limit" {
				input.Order.Limit = "0.10"
			} else {
				raw := `{"yes_dollars":[["0.55","8"]],"no_dollars":[]}`
				input.Books[0].Book = json.RawMessage(raw)
				input.Books[0].Original = `{"orderbook_fp":` + raw + `}`
				input.Books[0].SHA256 = simHash(input.Books[0].Original)
			}
			result := officialResult(input.Order.At.Add(25*time.Hour), true)
			for _, outcome := range []*storage.KalshiWeatherSettlement{nil, &result} {
				if _, err := CheckWeatherPaper(context.Background(), input, outcome, "synthetic_fixture"); err == nil || err.Error() != "diagnostic_requires_one_filled_contract" {
					t.Fatal("unfilled diagnostic did not fail at the fill acceptance boundary", err)
				}
			}
		})
	}
}
