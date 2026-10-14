package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"

	"git.subcult.tv/subculture-collective/mantik/internal/experiment"
	"git.subcult.tv/subculture-collective/mantik/internal/replay"
)

func runExperiment(ctx context.Context, args []string, out, errOut io.Writer) error {
	fs := flag.NewFlagSet("experiment", flag.ContinueOnError)
	fs.SetOutput(errOut)
	var recipePath, recipeHash, datasetPath, attemptID, root string
	var export bool
	fs.StringVar(&recipePath, "recipe", "", "sealed recipe JSON; omit for the built-in synthetic comparison")
	fs.StringVar(&recipeHash, "recipe-sha256", "", "expected recipe identity (required with recipe)")
	fs.StringVar(&datasetPath, "dataset", "", "dataset JSON pinned by the supplied recipe")
	fs.StringVar(&attemptID, "attempt", "", "immutable attempt id (required for execution)")
	fs.StringVar(&root, "out-dir", "", "local append-only artifact directory (required for execution)")
	fs.BoolVar(&export, "export-recipe", false, "print the built-in sealed recipe without execution")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if fs.NArg() != 0 {
		return errors.New("unexpected experiment positional arguments")
	}
	d, err := replay.FixtureDataset()
	if err != nil {
		return err
	}
	r, err := experiment.DefaultRecipe(d)
	if err != nil {
		return err
	}
	if export {
		if recipePath != "" || recipeHash != "" || datasetPath != "" || attemptID != "" || root != "" {
			return errors.New("export-recipe cannot be combined with execution flags")
		}
		return emit(out, r)
	}
	if root == "" || attemptID == "" {
		return errors.New("experiment requires out-dir and attempt")
	}
	if (recipePath == "") != (recipeHash == "") {
		return errors.New("recipe and recipe-sha256 must be provided together")
	}
	if datasetPath != "" && recipePath == "" {
		return errors.New("external dataset requires an explicit pinned recipe")
	}
	if recipePath != "" {
		file, err := os.Open(recipePath)
		if err != nil {
			return err
		}
		r, err = experiment.ReadRecipe(file)
		closeErr := file.Close()
		if err == nil {
			err = closeErr
		}
		if err != nil {
			return err
		}
		if r.SHA256 != recipeHash {
			return errors.New("recipe does not match expected identity")
		}
	}
	if datasetPath != "" {
		file, err := os.Open(datasetPath)
		if err != nil {
			return err
		}
		d, err = replay.Decode(file)
		closeErr := file.Close()
		if err == nil {
			err = closeErr
		}
		if err != nil {
			return err
		}
	}
	a, err := experiment.RunAndRetain(ctx, root, attemptID, r, d)
	if err != nil {
		return err
	}
	if err := emit(out, a); err != nil {
		return err
	}
	if a.Status != "completed" {
		return fmt.Errorf("experiment failed: %s (attempt retained)", a.ErrorCode)
	}
	return nil
}
