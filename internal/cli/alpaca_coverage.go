package cli

import (
	"context"
	"errors"
	"flag"
	"git.subcult.tv/subculture-collective/mantik/internal/alpaca"
	"io"
	"time"
)

func runAlpacaCoverage(ctx context.Context, args []string, out, errOut io.Writer) error {
	fs := flag.NewFlagSet("alpaca-coverage", flag.ContinueOnError)
	fs.SetOutput(errOut)
	var root string
	var spec alpaca.CoverageSpec
	fs.StringVar(&root, "journal", "", "existing private journal directory")
	fs.StringVar(&spec.Feed, "feed", "", "explicit quote feed (required)")
	fs.StringVar(&spec.Symbol, "symbol", "", "exact symbol (required)")
	fs.StringVar(&spec.EvidenceClass, "evidence-class", "prospective_market_unqualified", "exact retained evidence class")
	fs.Int64Var(&spec.MaxQuoteAgeSeconds, "max-quote-age-seconds", 60, "inspection quote age limit, never loosens the capture limit")
	fs.Int64Var(&spec.MaxCaptureAgeSeconds, "max-capture-age-seconds", 300, "maximum quote capture age")
	fs.Int64Var(&spec.MaxCalendarAgeSeconds, "max-calendar-age-seconds", 86400, "maximum calendar capture age")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if root == "" || fs.NArg() != 0 {
		return errors.New("coverage requires an existing journal and no positional arguments")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	journal, err := alpaca.OpenExistingJournal(root)
	if err != nil {
		return err
	}
	defer func() { _ = journal.Close() }()
	report, err := journal.Coverage(spec, time.Now().UTC())
	if err != nil {
		return err
	}
	return emit(out, report)
}
