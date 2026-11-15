package cli

import (
	"context"
	"errors"
	"flag"
	"io"
	"strings"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/alpaca"
	"git.subcult.tv/subculture-collective/mantik/internal/storage"
)

func runOptionsBackfill(ctx context.Context, args []string, out, errOut io.Writer) error {
	fs := flag.NewFlagSet("options-backfill", flag.ContinueOnError)
	fs.SetOutput(errOut)
	var database, journal, credentials, underlyings, start, end string
	spec := alpaca.OptionSpec{}
	fs.StringVar(&database, "database-url", "", "explicit isolated PostgreSQL URL")
	fs.StringVar(&journal, "journal", "", "dedicated absolute private history journal")
	fs.StringVar(&credentials, "credentials-file", "", "private key/secret or oauth_token JSON")
	fs.StringVar(&underlyings, "underlyings", "", "comma-separated liquid underlyings")
	fs.StringVar(&start, "start", "2024-02-01", "inclusive UTC date")
	fs.StringVar(&end, "end", "", "fixed exclusive UTC date")
	fs.StringVar(&spec.ExpiryStart, "expiry-start", "2024-02-01", "inclusive earliest expiration date")
	fs.StringVar(&spec.ExpiryEnd, "expiry-end", "", "inclusive latest expiration date (required)")
	fs.StringVar(&spec.MinStrike, "min-strike", "", "optional minimum strike in dollars")
	fs.StringVar(&spec.MaxStrike, "max-strike", "", "optional maximum strike in dollars")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if fs.NArg() != 0 || database == "" || journal == "" || credentials == "" || underlyings == "" || spec.ExpiryEnd == "" {
		return errors.New("options-backfill requires database-url, journal, credentials-file, underlyings, end and expiry-end")
	}
	var err error
	spec.Start, err = time.Parse(time.DateOnly, start)
	if err != nil {
		return errors.New("invalid option start date")
	}
	spec.End, err = time.Parse(time.DateOnly, end)
	if err != nil {
		return errors.New("invalid option end date")
	}
	spec.Underlyings = strings.Split(underlyings, ",")
	if err := spec.Validate(); err != nil {
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
	receipt, runErr := j.CollectOptions(ctx, client, store, spec, out)
	return errors.Join(emit(out, receipt), runErr)
}
