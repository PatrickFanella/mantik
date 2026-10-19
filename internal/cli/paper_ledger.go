package cli

import (
	"context"
	"errors"
	"flag"
	"git.subcult.tv/subculture-collective/mantik/internal/paperledger"
	"io"
	"os"
)

func runPaperLedger(ctx context.Context, args []string, out, errOut io.Writer) error {
	fs := flag.NewFlagSet("paper-ledger-audit", flag.ContinueOnError)
	fs.SetOutput(errOut)
	file := fs.String("file", "", "private synthetic paper ledger recording JSON")
	export := fs.Bool("export-fixture", false, "print an invented ledger recording without changing service accounts")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if fs.NArg() != 0 || (*file == "" && !*export) || (*file != "" && *export) {
		return errors.New("paper-ledger-audit requires exactly one file or export-fixture")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if *export {
		return emit(out, paperledger.Fixture())
	}
	f, err := os.Open(*file)
	if err != nil {
		return errors.New("cannot open synthetic paper ledger recording")
	}
	r, readErr := paperledger.Read(f)
	if err = errors.Join(readErr, f.Close()); err != nil {
		return err
	}
	result, err := paperledger.Audit(ctx, r)
	if err != nil {
		return err
	}
	return emit(out, result)
}
