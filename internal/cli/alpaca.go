package cli

import (
	"context"
	"errors"
	"flag"
	"io"

	"git.subcult.tv/subculture-collective/mantik/internal/alpaca"
)

func runAlpaca(ctx context.Context, args []string, out, errOut io.Writer) error {
	fs := flag.NewFlagSet("alpaca-normalize", flag.ContinueOnError)
	fs.SetOutput(errOut)
	var path, digest string
	fs.StringVar(&path, "recording", "", "retained Alpaca recording JSON file")
	fs.StringVar(&digest, "sha256", "", "expected SHA-256 of original provider response")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if fs.NArg() != 0 || path == "" || digest == "" {
		return errors.New("alpaca-normalize requires recording and expected sha256")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	record, err := readAlpacaRecording(path, digest)
	if err != nil {
		return err
	}

	result, err := alpaca.Normalize(record)
	if err != nil {
		return err
	}
	return emit(out, result)
}
