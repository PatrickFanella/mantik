package cli

import (
	"context"
	"errors"
	"flag"
	"io"
	"os"

	"git.subcult.tv/subculture-collective/mantik/internal/hypothesis"
)

func runHypothesis(ctx context.Context, args []string, out, errOut io.Writer) error {
	fs := flag.NewFlagSet("hypothesis-declare", flag.ContinueOnError)
	fs.SetOutput(errOut)
	file := fs.String("file", "", "private hypothesis declaration JSON file")
	method := fs.String("export-method", "", "print one supported native strategy configuration pin by ID")
	export := fs.Bool("export-fixture", false, "print an invented synthetic declaration without testing it")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	modes := 0
	if *file != "" {
		modes++
	}
	if *export {
		modes++
	}
	if *method != "" {
		modes++
	}
	if fs.NArg() != 0 || modes != 1 {
		return errors.New("hypothesis-declare requires exactly one file, export-fixture or export-method")
	}

	if err := ctx.Err(); err != nil {
		return err
	}
	if *method != "" {
		pin, definition, err := hypothesis.PinMethod(*method)
		if err != nil {
			return err
		}
		return emit(out, struct {
			Pin        hypothesis.MethodRef    `json:"pin"`
			Definition hypothesis.NativeMethod `json:"definition"`
		}{pin, definition})
	}
	if *export {
		return emit(out, hypothesis.Fixture())
	}
	f, err := os.Open(*file)
	if err != nil {
		return errors.New("cannot open hypothesis declaration")
	}
	declaration, readErr := hypothesis.Read(f)
	if err = errors.Join(readErr, f.Close()); err != nil {
		return err
	}
	result, err := hypothesis.Seal(declaration)
	if err != nil {
		return err
	}
	return emit(out, result)
}
