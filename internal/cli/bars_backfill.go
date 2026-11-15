package cli

import (
	"context"
	"errors"
	"flag"
	"io"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/alpaca"
	"git.subcult.tv/subculture-collective/mantik/internal/marketdata"
	"git.subcult.tv/subculture-collective/mantik/internal/storage"
)

func runBarsBackfill(ctx context.Context, args []string, out, errOut io.Writer) error {
	fs := flag.NewFlagSet("bars-backfill", flag.ContinueOnError)
	fs.SetOutput(errOut)
	var database, journal, credentials, assetsPath, startText, endText string
	fs.StringVar(&database, "database-url", "", "explicit isolated PostgreSQL URL (required)")
	fs.StringVar(&journal, "journal", "", "dedicated absolute private history journal")
	fs.StringVar(&credentials, "credentials-file", "", "mode-0600 key/secret or oauth_token JSON")
	fs.StringVar(&assetsPath, "assets", "", "JSON array of reviewed symbol/asset_id bindings")
	fs.StringVar(&startText, "start", "2016-01-01", "inclusive UTC date")
	fs.StringVar(&endText, "end", "", "fixed exclusive UTC date (required)")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if fs.NArg() != 0 || database == "" || journal == "" || credentials == "" || assetsPath == "" {
		return errors.New("bars-backfill requires database-url, journal, credentials-file, assets and end")
	}
	start, e1 := time.Parse(time.DateOnly, startText)
	end, e2 := time.Parse(time.DateOnly, endText)
	if e1 != nil || e2 != nil {
		return errors.New("backfill dates must be YYYY-MM-DD")
	}
	var assets []marketdata.BackfillAsset
	if err := readPaperJSON(assetsPath, &assets, false); err != nil {
		return err
	}
	if err := marketdata.ValidateBackfill(assets, start, end); err != nil {
		return err
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
	store, err := storage.OpenPostgres(ctx, database)
	if err != nil {
		return err
	}
	defer store.Close()
	return marketdata.BackfillDaily(ctx, store, func(ctx context.Context, symbol string, start, end time.Time) (alpaca.DailyBars, error) {
		return j.BackfillDailyBars(ctx, client, symbol, start, end)
	}, assets, start, end, out)
}
