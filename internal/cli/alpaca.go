package cli

import (
	"context"
	"errors"
	"flag"
	"io"
	"os"

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
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	record, readErr := alpaca.Read(file)
	closeErr := file.Close()
	if readErr != nil {
		return readErr
	}
	if closeErr != nil {
		return closeErr
	}
	if record.SHA256 != digest {
		return errors.New("recording does not match expected raw identity")
	}
	result, err := alpaca.Normalize(record)
	if err != nil {
		return err
	}
	return emit(out, result)
}
