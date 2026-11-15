package cli

import (
	"context"
	"errors"
	"flag"
	"io"
	"os"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/kalshi"
	"git.subcult.tv/subculture-collective/mantik/internal/storage"
)

func runKalshiWeather(ctx context.Context, command string, args []string, out, errOut io.Writer) error {
	fs := flag.NewFlagSet(command, flag.ContinueOnError)
	fs.SetOutput(errOut)
	database := fs.String("database-url", os.Getenv("DATABASE_URL"), "isolated Mantik PostgreSQL URL")
	var journal, ticker, version, cutoff string
	if command == "kalshi-weather-calibration" {
		fs.StringVar(&version, "model-version", kalshi.WeatherModelVersion, "one retained model version")
		fs.StringVar(&cutoff, "as-of", "", "optional RFC3339 availability cutoff (defaults to now)")
	} else {
		fs.StringVar(&journal, "journal", "", "absolute private raw capture directory")
		fs.StringVar(&ticker, "market-ticker", "", "required daily-high weather market ticker")
	}
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if *database == "" || fs.NArg() != 0 {
		return errors.New("weather command requires database-url and no positional arguments")
	}
	at := time.Now().UTC()
	if command == "kalshi-weather-calibration" {
		if version == "" {
			return errors.New("calibration requires a model version")
		}
		if cutoff != "" {
			var err error
			at, err = time.Parse(time.RFC3339Nano, cutoff)
			if err != nil {
				return errors.New("invalid calibration cutoff")
			}
		}
	} else if journal == "" || ticker == "" {
		return errors.New("weather command requires journal and market-ticker")
	}
	store, err := storage.OpenPostgres(ctx, *database)
	if err != nil {
		return err
	}
	defer store.Close()
	if command == "kalshi-weather-calibration" {
		report, err := kalshi.CalibrateWeather(ctx, store, version, at)
		if err != nil {
			return err
		}
		return emit(out, report)
	}
	client, err := kalshi.OpenClient(journal)
	if err != nil {
		return err
	}
	defer client.Close()
	if command == "kalshi-weather-predict" {
		receipt, err := client.PredictWeather(ctx, store, ticker)
		if err != nil {
			return err
		}
		return emit(out, receipt)
	}
	if err := client.SettleWeather(ctx, store, ticker); err != nil {
		return err
	}
	return emit(out, struct{ MarketTicker, Status string }{ticker, "final settlement retained"})
}
