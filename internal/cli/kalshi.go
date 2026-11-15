package cli

import (
	"context"
	"errors"
	"flag"
	"io"
	"os"

	"git.subcult.tv/subculture-collective/mantik/internal/kalshi"
	"git.subcult.tv/subculture-collective/mantik/internal/storage"
)

// runKalshiCollect collects one public Kalshi page. Credentials and provider
// endpoint overrides are intentionally absent: this is a public-read command.
func runKalshiCollect(ctx context.Context, args []string, out, errOut io.Writer) error {
	fs := flag.NewFlagSet("kalshi-collect", flag.ContinueOnError)
	fs.SetOutput(errOut)
	var databaseURL, series, cursor, journal string
	fs.StringVar(&databaseURL, "database-url", os.Getenv("DATABASE_URL"), "isolated Mantik PostgreSQL URL")
	fs.StringVar(&series, "series-ticker", "", "required weather series ticker")
	fs.StringVar(&journal, "journal", "", "absolute private raw capture directory")
	fs.StringVar(&cursor, "cursor", "", "optional prior public response cursor")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if fs.NArg() != 0 || databaseURL == "" || journal == "" || series == "" {
		return errors.New("kalshi-collect requires database-url, journal, series-ticker and no positional arguments")
	}
	store, err := storage.OpenPostgres(ctx, databaseURL)
	if err != nil {
		return err
	}
	defer store.Close()
	client, err := kalshi.OpenClient(journal)
	if err != nil {
		return err
	}
	defer client.Close()
	receipt, next, err := client.Collect(ctx, store, series, cursor)
	if err != nil {
		return err
	}
	return emit(out, struct {
		Receipt storage.KalshiReceipt `json:"receipt"`
		Cursor  string                `json:"cursor"`
	}{receipt, next})
}
