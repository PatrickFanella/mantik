package cli

import (
	"context"
	"errors"
	"flag"
	"io"
	"os"

	"git.subcult.tv/subculture-collective/mantik/internal/news"
)

func runNews(ctx context.Context, args []string, out, errOut io.Writer) error {
	fs := flag.NewFlagSet("news-normalize", flag.ContinueOnError)
	fs.SetOutput(errOut)
	file := fs.String("file", "", "local recorded news JSON file (required unless exporting fixture)")
	export := fs.Bool("export-fixture", false, "print an invented unqualified recording without normalization")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if fs.NArg() != 0 || *export && *file != "" {
		return errors.New("invalid news normalization arguments")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if *export {
		return emit(out, news.Fixture())
	}
	if *file == "" {
		return errors.New("news-normalize requires a local recording file")
	}
	f, err := os.Open(*file)
	if err != nil {
		return errors.New("cannot open news recording file")
	}
	r, readErr := news.Read(f)
	closeErr := f.Close()
	if err := errors.Join(readErr, closeErr); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	result, err := news.Normalize(r)
	if err != nil {
		return err
	}
	return emit(out, result)
}
