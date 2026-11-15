package kalshi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"testing"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/loopbacktest"
	"git.subcult.tv/subculture-collective/mantik/internal/storage"
)

type weatherTestStore struct {
	predictions []storage.KalshiWeatherPrediction
	settlements []storage.KalshiWeatherSettlement
	err         error
}

func (s *weatherTestStore) RecordKalshiWeatherPrediction(_ context.Context, p storage.KalshiWeatherPrediction) (storage.KalshiWeatherPrediction, error) {
	if s.err != nil {
		return p, s.err
	}
	p.RecordedAt = time.Now().UTC()
	s.predictions = append(s.predictions, p)
	return p, nil
}
func (s *weatherTestStore) RecordKalshiWeatherSettlement(_ context.Context, v storage.KalshiWeatherSettlement) error {
	s.settlements = append(s.settlements, v)
	return s.err
}
func (s *weatherTestStore) KalshiWeatherPredictions(context.Context, time.Time) ([]storage.KalshiWeatherPrediction, error) {
	return s.predictions, s.err
}
func (s *weatherTestStore) KalshiWeatherSettlements(context.Context, time.Time) ([]storage.KalshiWeatherSettlement, error) {
	return s.settlements, s.err
}

type rewriteWeatherTransport struct {
	target *url.URL
	base   http.RoundTripper
}

func (t rewriteWeatherTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	if r.Header.Get("Authorization") != "" {
		return nil, errors.New("weather request sent credentials")
	}
	copy := r.Clone(r.Context())
	u := *r.URL
	u.Scheme = t.target.Scheme
	u.Host = t.target.Host
	copy.URL = &u
	return t.base.RoundTrip(copy)
}

func weatherFixture(t *testing.T) (*Client, map[string]any, string, string) {
	return weatherSeriesFixture(t, "KXHIGHCHI")
}

func weatherSeriesFixture(t *testing.T, series string) (*Client, map[string]any, string, string) {
	t.Helper()
	now := time.Now().UTC()
	site := weatherSites[series]
	loc, err := time.LoadLocation(site.zone)
	if err != nil {
		t.Fatal(err)
	}
	local := now.In(loc)
	day := time.Date(local.Year(), local.Month(), local.Day()+1, 0, 0, 0, 0, loc)
	event := series + "-" + stringsUpperDate(day)
	ticker := event + "-T70"
	marshalTime := func(at time.Time) string { return at.Format(time.RFC3339) }
	fixtures := map[string]any{}
	fixtures["/trade-api/v2/markets/"+ticker] = map[string]any{"market": map[string]any{"ticker": ticker, "event_ticker": event, "status": "active", "close_time": marshalTime(day.AddDate(0, 0, 1)), "rules_primary": "maximum temperature at station (" + site.climate + ") in fahrenheit", "strike_type": "greater", "floor_strike": 70}}
	fixtures["/stations/"+site.station] = map[string]any{"geometry": map[string]any{"coordinates": []float64{-87.75, 41.78}}, "properties": map[string]any{"stationIdentifier": site.station}}
	fixtures["/points/41.7800,-87.7500"] = map[string]any{"properties": map[string]any{"forecastHourly": "https://api.weather.gov/gridpoints/LOT/1,2/forecast/hourly", "timeZone": site.zone}}
	periods := []map[string]any{}
	for at := now.Truncate(time.Hour); at.Before(day.AddDate(0, 0, 1)); at = at.Add(time.Hour) {
		periods = append(periods, map[string]any{"startTime": marshalTime(at), "endTime": marshalTime(at.Add(time.Hour)), "temperature": 70, "temperatureUnit": "F"})
	}
	fixtures["/gridpoints/LOT/1,2/forecast/hourly"] = map[string]any{"properties": map[string]any{"generatedAt": marshalTime(now.Add(-time.Minute)), "periods": periods}}
	fixtures["/stations/"+site.station+"/observations/latest"] = map[string]any{"properties": map[string]any{"station": "https://api.weather.gov/stations/" + site.station, "timestamp": marshalTime(now.Add(-30 * time.Minute)), "temperature": map[string]any{"value": 20, "unitCode": "wmoUnit:degC"}}}
	fixtures["/v1/forecast"] = map[string]any{"timezone": site.zone, "daily_units": map[string]any{"temperature_2m_max": "°F"}, "daily": map[string]any{"time": []string{day.Format("2006-01-02")}, "temperature_2m_max": []float64{72}}}
	server := httptest.NewServer(loopbacktest.GoClientOnly(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("User-Agent") == "" {
			t.Error("missing NOAA user agent")
		}
		v, ok := fixtures[r.URL.Path]
		if !ok {
			http.NotFound(w, r)
			return
		}
		if r.URL.Path == "/v1/forecast" && (r.URL.Query().Get("timezone") != site.zone || r.URL.Query().Get("start_date") != day.Format("2006-01-02") || r.URL.Query().Get("temperature_unit") != "fahrenheit") {
			t.Error("wrong forecast query", r.URL.RawQuery)
		}
		if err := json.NewEncoder(w).Encode(v); err != nil {
			t.Error(err)
		}
	})))
	t.Cleanup(server.Close)
	root := filepath.Join(t.TempDir(), "journal")
	c, err := OpenClient(root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := c.Close(); err != nil {
			t.Error(err)
		}
	})
	target, err := url.Parse(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	c.http.Transport = rewriteWeatherTransport{target, http.DefaultTransport}
	c.origin = server.URL + "/trade-api/v2"
	return c, fixtures, ticker, root
}
func stringsUpperDate(t time.Time) string {
	return fmt.Sprintf("%02d%s%02d", t.Year()%100, []string{"JAN", "FEB", "MAR", "APR", "MAY", "JUN", "JUL", "AUG", "SEP", "OCT", "NOV", "DEC"}[t.Month()-1], t.Day())
}

func TestWeatherPredictionRetainsInputsBeforeReturning(t *testing.T) {
	c, _, ticker, root := weatherFixture(t)
	s := &weatherTestStore{}
	got, err := c.PredictWeather(context.Background(), s, ticker)
	if err != nil {
		t.Fatal(err)
	}
	var p WeatherPrediction
	if err = json.Unmarshal(got.Payload, &p); err != nil {
		t.Fatal(err)
	}
	if len(s.predictions) != 1 || len(p.Evidence) != 6 || p.NOAAHighF != 70 || p.OpenMeteoHighF != 72 || p.StationF != 68 || p.MeanF != 71 || math.Abs(p.SigmaF-math.Sqrt(11)) > 1e-10 || p.Probability <= 0.5 || p.Probability >= 1 || got.ID != digest(got.Payload) || !got.RecordedAt.Before(got.CloseTime) {
		t.Fatal(got, p)
	}
	for _, e := range p.Evidence {
		if e.ObservedAt.After(p.AsOf) || digest([]byte(e.Original)) != e.SHA256 {
			t.Fatal("invalid evidence", e)
		}
	}
	files, err := os.ReadDir(root)
	if err != nil || len(files) != 6 {
		t.Fatal("lost raw captures", len(files), err)
	}
	s.err = errors.New("durability failure")
	if _, err = c.PredictWeather(context.Background(), s, ticker); !errors.Is(err, s.err) {
		t.Fatal("prediction escaped failed storage", err)
	}
}

func TestWeatherPredictionRejectsUnusableInputs(t *testing.T) {
	tests := []struct {
		name   string
		change func(map[string]any, string)
	}{
		{"closed", func(f map[string]any, ticker string) {
			f["/trade-api/v2/markets/"+ticker].(map[string]any)["market"].(map[string]any)["status"] = "closed"
		}},
		{"late", func(f map[string]any, ticker string) {
			f["/trade-api/v2/markets/"+ticker].(map[string]any)["market"].(map[string]any)["close_time"] = time.Now().Add(-time.Second).Format(time.RFC3339)
		}},
		{"wrong station rules", func(f map[string]any, ticker string) {
			f["/trade-api/v2/markets/"+ticker].(map[string]any)["market"].(map[string]any)["rules_primary"] = "temperature at another station"
		}},
		{"unsupported redirect target", func(f map[string]any, _ string) {
			f["/points/41.7800,-87.7500"].(map[string]any)["properties"].(map[string]any)["forecastHourly"] = "http://127.0.0.1/private"
		}},
		{"future NOAA issue", func(f map[string]any, _ string) {
			f["/gridpoints/LOT/1,2/forecast/hourly"].(map[string]any)["properties"].(map[string]any)["generatedAt"] = time.Now().Add(time.Hour).Format(time.RFC3339)
		}},
		{"stale NOAA issue", func(f map[string]any, _ string) {
			f["/gridpoints/LOT/1,2/forecast/hourly"].(map[string]any)["properties"].(map[string]any)["generatedAt"] = time.Now().Add(-7 * time.Hour).Format(time.RFC3339)
		}},
		{"missing hour", func(f map[string]any, _ string) {
			p := f["/gridpoints/LOT/1,2/forecast/hourly"].(map[string]any)["properties"].(map[string]any)
			periods := p["periods"].([]map[string]any)
			p["periods"] = periods[:len(periods)-1]
		}},
		{"future station observation", func(f map[string]any, _ string) {
			f["/stations/KMDW/observations/latest"].(map[string]any)["properties"].(map[string]any)["timestamp"] = time.Now().Add(time.Hour).Format(time.RFC3339)
		}},
		{"missing station temperature", func(f map[string]any, _ string) {
			f["/stations/KMDW/observations/latest"].(map[string]any)["properties"].(map[string]any)["temperature"].(map[string]any)["value"] = nil
		}},
		{"wrong meteo day", func(f map[string]any, _ string) {
			f["/v1/forecast"].(map[string]any)["daily"].(map[string]any)["time"] = []string{"2000-01-01"}
		}},
		{"null meteo temperature", func(f map[string]any, _ string) {
			f["/v1/forecast"].(map[string]any)["daily"].(map[string]any)["temperature_2m_max"] = []any{nil}
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			c, f, ticker, _ := weatherFixture(t)
			test.change(f, ticker)
			s := &weatherTestStore{}
			if _, err := c.PredictWeather(context.Background(), s, ticker); err == nil || len(s.predictions) != 0 {
				t.Fatal("invalid input produced a prediction", err)
			}
		})
	}
}

func TestWeatherProbabilityIntegerBounds(t *testing.T) {
	floor, cap := 70.0, 72.0
	cases := []struct {
		name string
		m    weatherMarket
		want float64
		bad  bool
	}{
		{"greater", weatherMarket{StrikeType: "greater", Floor: &floor}, 0.5 * math.Erfc(0.5/(3*math.Sqrt2)), false},
		{"less", weatherMarket{StrikeType: "less", Cap: &floor}, 0.5 * math.Erfc(0.5/(3*math.Sqrt2)), false},
		{"between", weatherMarket{StrikeType: "between", Floor: &floor, Cap: &cap}, 0.5*math.Erfc(-2.5/(3*math.Sqrt2)) - 0.5*math.Erfc(0.5/(3*math.Sqrt2)), false},
		{"missing", weatherMarket{StrikeType: "greater"}, 0, true},
		{"custom", weatherMarket{StrikeType: "custom", Floor: &floor}, 0, true},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			p, err := weatherProbability(tt.m, 70, 3)
			if (err != nil) != tt.bad || !tt.bad && math.Abs(p-tt.want) > 1e-12 {
				t.Fatal(p, err, tt.want)
			}
		})
	}
}

func TestWeatherSettlementRequiresFinalVenueResult(t *testing.T) {
	for _, status := range []string{"active", "closed", "settled", "finalized"} {
		t.Run(status, func(t *testing.T) {
			c, f, ticker, _ := weatherFixture(t)
			m := f["/trade-api/v2/markets/"+ticker].(map[string]any)["market"].(map[string]any)
			m["status"] = status
			m["result"] = "yes"
			m["close_time"] = time.Now().Add(-2 * time.Hour).Format(time.RFC3339)
			m["settlement_ts"] = time.Now().Add(-time.Hour).Format(time.RFC3339)
			s := &weatherTestStore{}
			err := c.SettleWeather(context.Background(), s, ticker)
			if (err == nil) != (status == "finalized") || (len(s.settlements) == 1) != (status == "finalized") {
				t.Fatal(err, s.settlements)
			}
		})
	}
}

func TestWeatherNewYorkStationAndCivilDay(t *testing.T) {
	c, _, ticker, _ := weatherSeriesFixture(t, "KXHIGHNY")
	s := &weatherTestStore{}
	got, err := c.PredictWeather(context.Background(), s, ticker)
	if err != nil {
		t.Fatal(err)
	}
	var p WeatherPrediction
	if err := json.Unmarshal(got.Payload, &p); err != nil {
		t.Fatal(err)
	}
	if p.Station != "KNYC" || p.TimeZone != "America/New_York" {
		t.Fatal(p)
	}

}

func TestNOAAWeatherDayDSTCoverage(t *testing.T) {
	for _, zone := range []string{"America/Chicago", "America/New_York"} {
		for _, tt := range []struct {
			name       string
			month      time.Month
			day, hours int
		}{{"spring", time.March, 8, 23}, {"fall", time.November, 1, 25}} {
			t.Run(zone+"/"+tt.name, func(t *testing.T) {
				loc, err := time.LoadLocation(zone)
				if err != nil {
					t.Fatal(err)
				}
				day := time.Date(2026, tt.month, tt.day, 0, 0, 0, 0, loc)
				end := day.AddDate(0, 0, 1)
				if end.Sub(day) != time.Duration(tt.hours)*time.Hour {
					t.Fatal("wrong civil-day length", day, end)
				}
				periods := []noaaWeatherPeriod{}
				// 100 F immediately outside either boundary must not enter the maximum.
				for at := day.Add(-time.Hour); !at.After(end); at = at.Add(time.Hour) {
					temp := 60.0
					if at.Before(day) || !at.Before(end) {
						temp = 100
					}
					periods = append(periods, noaaWeatherPeriod{Start: at, End: at.Add(time.Hour), Temperature: new(temp), Unit: "F"})
				}
				got, _, err := noaaDailyHigh(periods, day, day.Add(-30*time.Minute))
				if err != nil || got != 60 {
					t.Fatal("selected wrong civil-day hours", got, err)
				}
				// Omitting the repeated/transition day's last hour must fail coverage.
				missing := append([]noaaWeatherPeriod(nil), periods[:len(periods)-2]...)
				missing = append(missing, periods[len(periods)-1])
				if _, _, err := noaaDailyHigh(missing, day, day.Add(-30*time.Minute)); err == nil {
					t.Fatal("incomplete DST day admitted")
				}
			})
		}
	}
}
