package kalshi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/url"
	"regexp"
	"strings"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/storage"
)

// WeatherModelVersion identifies the fixed, unqualified baseline. Outcome
// calibration measures this version; it does not silently refit its parameters.
const WeatherModelVersion = "daily-high-normal-v1"

// WeatherEvidence retains the exact source bytes and the local availability clock.
type WeatherEvidence struct {
	URL, Original, SHA256 string
	ObservedAt            time.Time
}

// WeatherPrediction is advisory research, with no order or allocation authority.
type WeatherPrediction struct {
	ModelVersion, MarketTicker, Station, Date, TimeZone             string
	AsOf, CloseTime                                                 time.Time
	NOAAHighF, OpenMeteoHighF, StationF, MeanF, SigmaF, Probability float64
	Evidence                                                        []WeatherEvidence
}

type weatherSite struct{ station, climate, zone string }

var weatherSites = map[string]weatherSite{
	"KXHIGHCHI": {"KMDW", "CLIMDW", "America/Chicago"},
	"KXHIGHNY":  {"KNYC", "CLINYC", "America/New_York"},
}

// WeatherStore provides durable pre-close admission and append-only settlement.
type WeatherStore interface {
	RecordKalshiWeatherPrediction(context.Context, storage.KalshiWeatherPrediction) (storage.KalshiWeatherPrediction, error)
	RecordKalshiWeatherSettlement(context.Context, storage.KalshiWeatherSettlement) error
	KalshiWeatherPredictions(context.Context, time.Time) ([]storage.KalshiWeatherPrediction, error)
	KalshiWeatherSettlements(context.Context, time.Time) ([]storage.KalshiWeatherSettlement, error)
}

type weatherMarket struct {
	Ticker         string    `json:"ticker"`
	EventTicker    string    `json:"event_ticker"`
	Status         string    `json:"status"`
	CloseTime      time.Time `json:"close_time"`
	Rules          string    `json:"rules_primary"`
	StrikeType     string    `json:"strike_type"`
	Floor          *float64  `json:"floor_strike"`
	Cap            *float64  `json:"cap_strike"`
	Result         string    `json:"result"`
	SettlementTime time.Time `json:"settlement_ts"`
}

func (c *Client) weatherRead(ctx context.Context, target string, wire any) (WeatherEvidence, error) {
	raw, at, err := c.fetchURL(ctx, target)
	if err != nil {
		return WeatherEvidence{}, err
	}
	e := WeatherEvidence{target, string(raw), digest(raw), at}
	if err := json.Unmarshal(raw, wire); err != nil {
		return e, fmt.Errorf("invalid weather source JSON: %w", err)
	}
	return e, nil
}

func (c *Client) weatherMarket(ctx context.Context, ticker string) (weatherMarket, WeatherEvidence, error) {
	if !validTicker(ticker) {
		return weatherMarket{}, WeatherEvidence{}, errors.New("invalid weather ticker")
	}
	var wire struct {
		Market weatherMarket `json:"market"`
	}
	e, err := c.weatherRead(ctx, strings.TrimRight(c.origin, "/")+"/markets/"+url.PathEscape(ticker), &wire)
	if err == nil && (wire.Market.Ticker != ticker || wire.Market.CloseTime.IsZero()) {
		err = errors.New("weather market identity or close time missing")
	}
	return wire.Market, e, err
}

// PredictWeather fetches only current public inputs, derives a probability, and
// returns it only after the store has admitted it before market close.
func (c *Client) PredictWeather(ctx context.Context, store WeatherStore, ticker string) (storage.KalshiWeatherPrediction, error) {
	if c.journal == nil {
		return storage.KalshiWeatherPrediction{}, errors.New("weather prediction requires a raw capture journal")
	}
	p, err := c.forecastWeather(ctx, ticker)
	if err != nil {
		return storage.KalshiWeatherPrediction{}, err
	}
	raw, err := json.Marshal(p)
	if err != nil {
		return storage.KalshiWeatherPrediction{}, err
	}
	return store.RecordKalshiWeatherPrediction(ctx, storage.KalshiWeatherPrediction{
		ID: digest(raw), MarketTicker: ticker, ModelVersion: p.ModelVersion, AsOf: p.AsOf,
		CloseTime: p.CloseTime, Probability: p.Probability, Payload: raw,
	})
}

func (c *Client) forecastWeather(ctx context.Context, ticker string) (WeatherPrediction, error) {
	fail := func(err error) (WeatherPrediction, error) { return WeatherPrediction{}, err }
	m, evidence, err := c.weatherMarket(ctx, ticker)
	if err != nil {
		return fail(err)
	}
	series, datePart, ok := strings.Cut(m.EventTicker, "-")
	site, supported := weatherSites[series]
	if !ok || !supported || !strings.HasPrefix(ticker, m.EventTicker+"-") || !strings.Contains(m.Rules, site.climate) || !strings.Contains(strings.ToLower(m.Rules), "maximum temperature") || !strings.Contains(strings.ToLower(m.Rules), "fahrenheit") {
		return fail(errors.New("unsupported daily-high market or station rules"))
	}
	loc, err := time.LoadLocation(site.zone)
	if err != nil {
		return fail(err)
	}
	day, err := time.ParseInLocation("06Jan02", datePart, loc)
	if err != nil || strings.ToUpper(day.Format("06Jan02")) != datePart {
		return fail(errors.New("invalid weather event date"))
	}
	// Use the station city's civil date, including 23/25-hour DST days.
	// Restrict the baseline to the next day; do not reconstruct past inputs.
	now := time.Now().UTC()
	if day.Format("2006-01-02") != now.In(loc).AddDate(0, 0, 1).Format("2006-01-02") || !now.Before(m.CloseTime) || (m.Status != "active" && m.Status != "open") || m.Result != "" {
		return fail(errors.New("weather baseline requires an open next-day market"))
	}
	p := WeatherPrediction{ModelVersion: WeatherModelVersion, MarketTicker: ticker, Station: site.station, Date: day.Format("2006-01-02"), TimeZone: site.zone, CloseTime: m.CloseTime, Evidence: []WeatherEvidence{evidence}}
	var station struct {
		Geometry struct {
			Coordinates []float64 `json:"coordinates"`
		} `json:"geometry"`
		Properties struct {
			ID string `json:"stationIdentifier"`
		} `json:"properties"`
	}
	e, err := c.weatherRead(ctx, "https://api.weather.gov/stations/"+site.station, &station)
	if err != nil {
		return fail(err)
	}
	p.Evidence = append(p.Evidence, e)
	if station.Properties.ID != site.station || len(station.Geometry.Coordinates) != 2 || !finite(station.Geometry.Coordinates[0]) || !finite(station.Geometry.Coordinates[1]) || math.Abs(station.Geometry.Coordinates[0]) > 180 || math.Abs(station.Geometry.Coordinates[1]) > 90 {
		return fail(errors.New("invalid NOAA station location"))
	}
	lon, lat := station.Geometry.Coordinates[0], station.Geometry.Coordinates[1]
	var point struct {
		Properties struct {
			Hourly   string `json:"forecastHourly"`
			TimeZone string `json:"timeZone"`
		} `json:"properties"`
	}
	e, err = c.weatherRead(ctx, fmt.Sprintf("https://api.weather.gov/points/%.4f,%.4f", lat, lon), &point)
	if err != nil {
		return fail(err)
	}
	p.Evidence = append(p.Evidence, e)
	// Follow only the documented NOAA hourly path, never arbitrary upstream URLs.
	if !noaaHourlyURL.MatchString(point.Properties.Hourly) || point.Properties.TimeZone != site.zone {
		return fail(errors.New("unsupported NOAA hourly URL"))
	}
	var hourly struct {
		Properties struct {
			Generated time.Time           `json:"generatedAt"`
			Periods   []noaaWeatherPeriod `json:"periods"`
		} `json:"properties"`
	}
	e, err = c.weatherRead(ctx, point.Properties.Hourly+"?units=us", &hourly)
	if err != nil {
		return fail(err)
	}
	p.Evidence = append(p.Evidence, e)
	if hourly.Properties.Generated.IsZero() || hourly.Properties.Generated.After(e.ObservedAt) || e.ObservedAt.Sub(hourly.Properties.Generated) > 6*time.Hour {
		return fail(errors.New("stale or future NOAA forecast"))
	}
	var currentF float64
	p.NOAAHighF, currentF, err = noaaDailyHigh(hourly.Properties.Periods, day, e.ObservedAt)
	if err != nil {
		return fail(err)
	}
	var observation struct {
		Properties struct {
			Station     string    `json:"station"`
			Timestamp   time.Time `json:"timestamp"`
			Temperature struct {
				Value *float64 `json:"value"`
				Unit  string   `json:"unitCode"`
			} `json:"temperature"`
		} `json:"properties"`
	}
	e, err = c.weatherRead(ctx, "https://api.weather.gov/stations/"+site.station+"/observations/latest", &observation)
	if err != nil {
		return fail(err)
	}
	p.Evidence = append(p.Evidence, e)
	o := observation.Properties
	if o.Station != "https://api.weather.gov/stations/"+site.station || o.Timestamp.IsZero() || o.Timestamp.After(e.ObservedAt) || e.ObservedAt.Sub(o.Timestamp) > 2*time.Hour || o.Temperature.Value == nil || !finite(*o.Temperature.Value) || o.Temperature.Unit != "wmoUnit:degC" {
		return fail(errors.New("invalid, stale or future NOAA station observation"))
	}
	p.StationF = *o.Temperature.Value*9/5 + 32
	q := url.Values{"latitude": {fmt.Sprint(lat)}, "longitude": {fmt.Sprint(lon)}, "daily": {"temperature_2m_max"}, "temperature_unit": {"fahrenheit"}, "timezone": {site.zone}, "start_date": {p.Date}, "end_date": {p.Date}}
	var meteo struct {
		TimeZone   string `json:"timezone"`
		DailyUnits struct {
			Max string `json:"temperature_2m_max"`
		} `json:"daily_units"`
		Daily struct {
			Time []string   `json:"time"`
			Max  []*float64 `json:"temperature_2m_max"`
		} `json:"daily"`
	}
	e, err = c.weatherRead(ctx, "https://api.open-meteo.com/v1/forecast?"+q.Encode(), &meteo)
	if err != nil {
		return fail(err)
	}
	p.Evidence = append(p.Evidence, e)
	if meteo.TimeZone != site.zone || meteo.DailyUnits.Max != "°F" || len(meteo.Daily.Time) != 1 || meteo.Daily.Time[0] != p.Date || len(meteo.Daily.Max) != 1 || meteo.Daily.Max[0] == nil || !finite(*meteo.Daily.Max[0]) {
		return fail(errors.New("invalid Open-Meteo daily forecast"))
	}
	p.OpenMeteoHighF = *meteo.Daily.Max[0]
	p.MeanF = (p.NOAAHighF + p.OpenMeteoHighF) / 2
	// Disagreement between forecasts and a current station/grid mismatch widen
	// uncertainty. The 3 F floor is a declared baseline, not a fitted error rate.
	p.SigmaF = math.Sqrt(9 + math.Pow((p.NOAAHighF-p.OpenMeteoHighF)/2, 2) + math.Pow((p.StationF-currentF)/2, 2))
	p.Probability, err = weatherProbability(m, p.MeanF, p.SigmaF)
	if err != nil {
		return fail(err)
	}
	p.AsOf = time.Now().UTC().Truncate(time.Microsecond)
	if !p.AsOf.Before(p.CloseTime) || !p.AsOf.Before(day) {
		return fail(errors.New("weather prediction crossed its cutoff"))
	}
	for _, input := range p.Evidence {
		if input.ObservedAt.After(p.AsOf) {
			return fail(errors.New("future weather input"))
		}
	}
	return p, nil
}

type noaaWeatherPeriod struct {
	Start       time.Time `json:"startTime"`
	End         time.Time `json:"endTime"`
	Temperature *float64  `json:"temperature"`
	Unit        string    `json:"temperatureUnit"`
}

func noaaDailyHigh(periods []noaaWeatherPeriod, day, observed time.Time) (float64, float64, error) {
	covered := day
	high := math.Inf(-1)
	var currentF float64
	var currentAt time.Time
	for _, h := range periods {
		if h.Temperature == nil || !finite(*h.Temperature) || h.Unit != "F" || h.End.Sub(h.Start) != time.Hour {
			return 0, 0, errors.New("invalid NOAA hourly temperature")
		}
		if !observed.Before(h.Start) && observed.Before(h.End) {
			currentF = *h.Temperature
			currentAt = h.Start
		}
		if h.Start.Before(day) || !h.Start.Before(day.AddDate(0, 0, 1)) {
			continue
		}
		if !h.Start.Equal(covered) {
			return 0, 0, errors.New("incomplete NOAA daily coverage")
		}
		covered = h.End
		high = math.Max(high, *h.Temperature)
	}
	if !covered.Equal(day.AddDate(0, 0, 1)) || currentAt.IsZero() {
		return 0, 0, errors.New("NOAA forecast does not cover current hour and full target day")
	}
	return high, currentF, nil
}

var noaaHourlyURL = regexp.MustCompile(`^https://api\.weather\.gov/gridpoints/[A-Z]{3}/[0-9]+,[0-9]+/forecast/hourly$`)

func finite(v float64) bool { return !math.IsNaN(v) && !math.IsInf(v, 0) }

func weatherProbability(m weatherMarket, mean, sigma float64) (float64, error) {
	if !finite(mean) || !finite(sigma) || sigma <= 0 {
		return 0, errors.New("invalid weather distribution")
	}
	// Kalshi daily-high outcomes are integer Fahrenheit. Apply half-degree
	// continuity corrections to the inclusive integer resolution ranges.
	cdf := func(x float64) float64 { return 0.5 * math.Erfc(-(x-mean)/(sigma*math.Sqrt2)) }
	valid := func(x *float64) bool { return x != nil && finite(*x) && *x == math.Trunc(*x) && math.Abs(*x) <= 200 }
	switch m.StrikeType {
	case "greater":
		if valid(m.Floor) && m.Cap == nil {
			return 1 - cdf(*m.Floor+0.5), nil
		}
	case "less":
		if valid(m.Cap) && m.Floor == nil {
			return cdf(*m.Cap - 0.5), nil
		}
	case "between":
		if valid(m.Floor) && valid(m.Cap) && *m.Floor <= *m.Cap {
			return cdf(*m.Cap+0.5) - cdf(*m.Floor-0.5), nil
		}
	}
	return 0, errors.New("unsupported weather strike bounds")
}

// SettleWeather records only the venue's final yes/no result, never a forecast
// or preliminary NOAA observation substituted for official settlement.
func (c *Client) SettleWeather(ctx context.Context, store WeatherStore, ticker string) error {
	r, err := c.FetchWeatherSettlement(ctx, ticker)
	if err != nil {
		return err
	}
	return store.RecordKalshiWeatherSettlement(ctx, r)
}

// FetchWeatherSettlement retains and validates a public final outcome without
// writing a prediction or backdating its admission. Offline paper replays use
// this same source contract after their original pre-close receipt is sealed.
func (c *Client) FetchWeatherSettlement(ctx context.Context, ticker string) (storage.KalshiWeatherSettlement, error) {
	if c.journal == nil {
		return storage.KalshiWeatherSettlement{}, errors.New("weather settlement requires a raw capture journal")
	}
	m, e, err := c.weatherMarket(ctx, ticker)
	if err != nil {
		return storage.KalshiWeatherSettlement{}, err
	}
	if m.Status != "finalized" || (m.Result != "yes" && m.Result != "no") || m.SettlementTime.IsZero() || m.SettlementTime.Before(m.CloseTime) || m.SettlementTime.After(e.ObservedAt) {
		return storage.KalshiWeatherSettlement{}, errors.New("weather market has no final binary settlement")
	}
	return storage.KalshiWeatherSettlement{MarketTicker: ticker, Yes: m.Result == "yes", SettledAt: m.SettlementTime, ObservedAt: e.ObservedAt, Original: e.Original, SHA256: e.SHA256}, nil
}
