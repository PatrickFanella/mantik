// Command kalshicheck retains a bounded diagnostic notional weather trade and
// later joins its official settlement to the original pre-close forecast.
package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/kalshi"
	"git.subcult.tv/subculture-collective/mantik/internal/storage"
)

// captureStore admits one forecast durably before close. This private check
// does not reopen or backfill the production PostgreSQL prediction history.
type captureStore struct {
	*storage.Memory
	root        string
	predictions []storage.KalshiWeatherPrediction
	now         func() time.Time
}

func (s *captureStore) RecordKalshiWeatherPrediction(_ context.Context, p storage.KalshiWeatherPrediction) (storage.KalshiWeatherPrediction, error) {
	now := s.now
	if now == nil {
		now = time.Now
	}
	p.RecordedAt = now().UTC()
	if p.AsOf.After(p.RecordedAt) || !p.RecordedAt.Before(p.CloseTime) {
		return p, errors.New("forecast not admitted before close")
	}
	pending := filepath.Join(s.root, "prediction-attempt.json")
	if err := save(pending, p); err != nil {
		return p, err
	}
	if !now().UTC().Before(p.CloseTime) {
		return p, errors.New("forecast durability completed after close")
	}
	path := filepath.Join(s.root, "prediction.json")
	if err := os.Link(pending, path); err != nil {
		return p, err
	}
	dir, err := os.Open(s.root)
	if err != nil {
		return p, errors.Join(err, os.Remove(path))
	}
	defer func() { _ = dir.Close() }()
	if err := dir.Sync(); err != nil {
		return p, errors.Join(err, os.Remove(path), dir.Sync())
	}
	if !now().UTC().Before(p.CloseTime) {
		// Remove only the just-published link, retaining the original attempted
		// bytes under their explicitly non-admitted name.
		return p, errors.Join(errors.New("forecast publication completed after close"), os.Remove(path), dir.Sync())
	}
	s.predictions = append(s.predictions, p)
	return p, nil
}
func (s *captureStore) RecordKalshiWeatherSettlement(context.Context, storage.KalshiWeatherSettlement) error {
	return errors.New("start cannot settle an outcome")
}
func (s *captureStore) KalshiWeatherPredictions(context.Context, time.Time) ([]storage.KalshiWeatherPrediction, error) {
	return s.predictions, nil
}
func (s *captureStore) KalshiWeatherSettlements(context.Context, time.Time) ([]storage.KalshiWeatherSettlement, error) {
	return nil, nil
}

func save(path string, value any) error {
	// Forecast IDs bind the compact JSON emitted by PredictWeather. Indenting a
	// RawMessage would change those payload bytes while keeping the old ID.
	raw, err := json.Marshal(value)
	if err != nil {
		return err
	}
	raw = append(raw, '\n')
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	if _, err = f.Write(raw); err == nil {
		err = f.Sync()
	}
	err = errors.Join(err, f.Close())
	if err != nil {
		return err
	}
	dir, err := os.Open(filepath.Dir(path))
	if err != nil {
		return err
	}
	defer func() { _ = dir.Close() }()
	return dir.Sync()
}

func read(path string, value any) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer func() { _ = f.Close() }()
	raw, err := io.ReadAll(io.LimitReader(f, (8<<20)+1))
	if err != nil {
		return "", err
	}
	if len(raw) > 8<<20 {
		return "", errors.New("paper check exceeds input bound")
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]), json.Unmarshal(raw, value)
}

func run(ctx context.Context, args []string, out, errOut io.Writer) error {
	fs := flag.NewFlagSet("kalshicheck", flag.ContinueOnError)
	fs.SetOutput(errOut)
	action := fs.String("action", "show", "start, admit, settle, or show")
	root := fs.String("root", "", "absolute private directory for this one diagnostic")
	ticker := fs.String("ticker", "", "explicit next-day supported weather market")
	side := fs.String("side", "", "explicit diagnostic buy side: yes or no")
	limit := fs.String("limit", "", "explicit diagnostic limit in dollars")
	pin := fs.String("input-sha256", "", "required sealed input hash for settle/show")
	databaseEnv := fs.String("database-env", "", "explicit isolated PostgreSQL URL environment variable; admit mirrors the original forecast before close")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if fs.NArg() != 0 || !filepath.IsAbs(*root) || (*action != "start" && *action != "admit" && *action != "settle" && *action != "show") {
		return errors.New("invalid paper check action or root")
	}
	if err := os.Mkdir(*root, 0700); err != nil && !errors.Is(err, os.ErrExist) {
		return err
	}
	info, err := os.Lstat(*root)
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode().Perm() != 0700 {
		return errors.New("paper check requires private nonsymlink directory")
	}
	inputPath := filepath.Join(*root, "input.json")
	if *action == "start" {
		if _, err := os.Lstat(inputPath); !errors.Is(err, os.ErrNotExist) {
			return errors.New("paper input already exists or cannot be inspected")
		}
		if *ticker == "" || (*side != "yes" && *side != "no") || *limit == "" {
			return errors.New("start requires ticker, side and limit")
		}
		client, err := kalshi.OpenClient(filepath.Join(*root, "captures"))
		if err != nil {
			return err
		}
		defer func() { _ = client.Close() }()
		store := &captureStore{Memory: storage.NewMemory(), root: *root}
		series, _, ok := strings.Cut(*ticker, "-")
		if !ok {
			return errors.New("invalid weather ticker")
		}
		_, cursor, err := client.Collect(ctx, store, series, "")
		if err != nil {
			return err
		}
		if cursor != "" {
			return errors.New("market capture incomplete: pagination required")
		}
		prediction, err := client.PredictWeather(ctx, store, *ticker)
		if err != nil {
			return err
		}
		event, err := client.FetchSimulationEvent(ctx, strings.Join(strings.Split(*ticker, "-")[:2], "-"))
		if err != nil {
			return err
		}
		order := kalshi.SimOrder{ID: "diagnostic-one-contract", MarketTicker: *ticker, Side: *side, Action: "buy", Limit: *limit, Contracts: 1, At: time.Now().UTC()}
		book, err := client.FetchOrderBook(ctx, *ticker)
		if err != nil {
			return err
		}
		if _, err = store.RecordKalshiData(ctx, nil, []storage.KalshiOrderBook{{MarketTicker: book.MarketTicker, Original: book.Original, SHA256: book.SHA256, Book: book.Book, ObservedAt: book.ObservedAt}}); err != nil {
			return err
		}
		at := time.Now().UTC()
		markets, err := store.KalshiMarketsAsOf(ctx, at)
		if err != nil {
			return err
		}
		books, err := store.KalshiBooksAsOf(ctx, *ticker, at)
		if err != nil {
			return err
		}
		input := kalshi.PaperCheckInput{Version: "weather-paper-check-v1", Prediction: prediction, Markets: markets, Books: books, Event: event, Order: order, ExecuteAt: at}
		if err := save(inputPath, input); err != nil {
			return err
		}
	}
	var input kalshi.PaperCheckInput
	actual, err := read(inputPath, &input)
	if err != nil {
		return err
	}
	if *action != "start" && (*pin == "" || *pin != actual) {
		return errors.New("sealed input hash differs")
	}
	var durable *storage.Postgres
	if *databaseEnv != "" {
		url := os.Getenv(*databaseEnv)
		if url == "" {
			return errors.New("named isolated database URL is absent")
		}
		durable, err = storage.OpenPostgres(ctx, url)
		if err != nil {
			return err
		}
		defer durable.Close()
	}
	if *action == "admit" {
		if durable == nil {
			return errors.New("admit requires an explicitly selected isolated database")
		}
		if _, err = kalshi.CheckWeatherPaper(ctx, input, nil, "prospective_market_unqualified"); err != nil {
			return err
		}
		// This is a new canonical storage receipt NOW, not a claim that the
		// original local diagnostic used this later SQL admission.
		p, err := durable.RecordKalshiWeatherPrediction(ctx, input.Prediction)
		if err != nil {
			return err
		}
		path := filepath.Join(*root, "canonical-admission.json")
		if _, err := os.Lstat(path); errors.Is(err, os.ErrNotExist) {
			if err := save(path, p); err != nil {
				return err
			}
		} else if err != nil {
			return err
		}
	}
	if durable != nil && *action == "settle" {
		predictions, err := durable.KalshiWeatherPredictions(ctx, time.Now().UTC())
		if err != nil {
			return err
		}
		found := false
		for _, p := range predictions {
			if p.ID == input.Prediction.ID {
				found = true
			}
		}
		if !found {
			return errors.New("canonical pre-close prediction is absent; cannot import a late forecast")
		}
	}
	var settlement *storage.KalshiWeatherSettlement
	settlementPath := filepath.Join(*root, "settlement.json")
	if _, err := os.Lstat(settlementPath); err == nil {
		settlement = &storage.KalshiWeatherSettlement{}
		if _, err = read(settlementPath, settlement); err != nil {
			return err
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if *action == "settle" && settlement == nil {
		// Validate the sealed pre-close input before any fresh provider read.
		if _, err = kalshi.CheckWeatherPaper(ctx, input, nil, "prospective_market_unqualified"); err != nil {
			return err
		}
		client, err := kalshi.OpenClient(filepath.Join(*root, "captures"))
		if err != nil {
			return err
		}
		defer func() { _ = client.Close() }()
		r, err := client.FetchWeatherSettlement(ctx, input.Order.MarketTicker)
		if err != nil {
			return err
		}
		if _, err = kalshi.CheckWeatherPaper(ctx, input, &r, "prospective_market_unqualified"); err != nil {
			return err
		}
		if durable != nil {
			if err := durable.RecordKalshiWeatherSettlement(ctx, r); err != nil {
				return err
			}
			rows, err := durable.KalshiWeatherSettlements(ctx, time.Now().UTC())
			if err != nil {
				return err
			}
			for _, retained := range rows {
				if retained.MarketTicker == r.MarketTicker {
					r = retained
					break
				}
			}
		}
		if err = save(settlementPath, r); err != nil {
			return err
		}
		settlement = &r
	}
	report, err := kalshi.CheckWeatherPaper(ctx, input, settlement, "prospective_market_unqualified")
	if err != nil {
		return err
	}
	var calibration *kalshi.WeatherCalibration
	if durable != nil {
		if settlement != nil && *action == "settle" {
			if err := durable.RecordKalshiWeatherSettlement(ctx, *settlement); err != nil {
				return err
			}
		}
		value, err := kalshi.CalibrateWeather(ctx, durable, input.Prediction.ModelVersion, time.Now().UTC())
		if err != nil {
			return err
		}
		calibration = &value
		path := filepath.Join(*root, "canonical-calibration-"+*action+".json")
		if _, err := os.Lstat(path); errors.Is(err, os.ErrNotExist) {
			if err := save(path, value); err != nil {
				return err
			}
		} else if err != nil {
			return err
		}
	}
	if *action == "start" {
		if err := save(filepath.Join(*root, "opening-report.json"), report); err != nil {
			return err
		}
	}
	if *action == "settle" {
		path := filepath.Join(*root, "settled-report.json")
		if _, err := os.Lstat(path); errors.Is(err, os.ErrNotExist) {
			if err := save(path, report); err != nil {
				return err
			}
		} else if err != nil {
			return err
		}
	}
	return json.NewEncoder(out).Encode(struct {
		InputSHA256          string
		Report               kalshi.PaperCheckReport
		CanonicalCalibration *kalshi.WeatherCalibration
	}{actual, report, calibration})
}

func main() {
	if err := run(context.Background(), os.Args[1:], os.Stdout, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
