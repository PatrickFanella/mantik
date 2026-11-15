package cli

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"io"
	"os"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/alpaca"
	"git.subcult.tv/subculture-collective/mantik/internal/marketdata"
	"git.subcult.tv/subculture-collective/mantik/internal/storage"
)

func runStockUniverse(ctx context.Context, command string, args []string, out, errOut io.Writer) error {
	fs := flag.NewFlagSet(command, flag.ContinueOnError)
	fs.SetOutput(errOut)
	var database, reference, clock, credentials, journal string
	cfg := marketdata.DefaultUniverseConfig()
	if command == "stock-assets" {
		fs.StringVar(&credentials, "credentials-file", "", "mode-0600 key/secret or oauth_token JSON")
		fs.StringVar(&journal, "journal", "", "absolute private asset-reference journal")
	} else {
		fs.StringVar(&database, "database-url", "", "explicit isolated PostgreSQL URL")
		fs.StringVar(&reference, "reference", "", "retained stock-assets JSON output")
		fs.StringVar(&clock, "as-of", "", "RFC3339 membership clock")
		fs.StringVar(&cfg.MinPrice, "min-price", cfg.MinPrice, "minimum closing price in dollars")
		fs.StringVar(&cfg.MinAverageDollarVolume, "min-dollar-volume", cfg.MinAverageDollarVolume, "minimum trailing average close*volume in dollars")
		fs.IntVar(&cfg.Sessions, "sessions", cfg.Sessions, "required trailing daily sessions")
		fs.IntVar(&cfg.MaxAgeDays, "max-age-days", cfg.MaxAgeDays, "maximum age of latest session in calendar days")
	}
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if fs.NArg() != 0 {
		return errors.New("stock commands accept no positional arguments")
	}
	if command == "stock-assets" {
		if credentials == "" || journal == "" {
			return errors.New("stock-assets requires credentials-file and journal")
		}
		var creds alpaca.PaperCredentials
		if err := readPaperJSON(credentials, &creds, true); err != nil {
			return err
		}
		client, err := alpaca.NewReadClient(creds, "sip")
		if err != nil {
			return err
		}
		j, err := alpaca.OpenJournal(journal)
		if err != nil {
			return err
		}
		defer func() { _ = j.Close() }()
		result, err := j.StockAssets(ctx, client)
		if err != nil {
			return err
		}
		return emit(out, result)
	}
	at, err := time.Parse(time.RFC3339, clock)
	if err != nil || reference == "" || database == "" {
		return errors.New("stock-universe requires reference, database-url and RFC3339 as-of")
	}
	f, err := os.Open(reference)
	if err != nil {
		return err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Size() > 32<<20 {
		return errors.New("asset reference must be a regular file of at most 32 MiB")
	}
	var snapshot marketdata.AssetSnapshot
	decoder := json.NewDecoder(io.LimitReader(f, (32<<20)+1))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&snapshot); err != nil {
		return errors.New("invalid asset reference JSON")
	}
	if decoder.Decode(new(any)) != io.EOF {
		return errors.New("expected one asset reference")
	}
	store, err := storage.OpenPostgres(ctx, database)
	if err != nil {
		return err
	}
	defer store.Close()
	result, err := marketdata.StockUniverse(ctx, store, snapshot, at, cfg)
	if err != nil {
		return err
	}
	return emit(out, result)
}
