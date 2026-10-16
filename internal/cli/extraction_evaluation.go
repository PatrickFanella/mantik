package cli

import (
	"context"
	"errors"
	"flag"
	"io"

	"git.subcult.tv/subculture-collective/mantik/internal/extraction"
	"git.subcult.tv/subculture-collective/mantik/internal/workerplan"
)

func runExtractionEvaluation(ctx context.Context, args []string, out, errOut io.Writer) error {
	fs := flag.NewFlagSet("news-extraction-evaluate", flag.ContinueOnError)
	fs.SetOutput(errOut)
	path := fs.String("corpus", "", "bounded local corpus JSON with recorded artifacts and expected responses")
	fixture := fs.Bool("export-fixture", false, "print an invented corpus; no extractor quality claim")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if fs.NArg() != 0 || *fixture && *path != "" || !*fixture && *path == "" {
		return errors.New("news-extraction-evaluate requires corpus or export-fixture")
	}
	if *fixture {
		return emit(out, extraction.FixtureCorpus())
	}
	raw, err := readExtractionFile(*path, extraction.MaxEvaluationBytes)
	if err != nil {
		return err
	}
	var corpus extraction.Corpus
	if workerplan.ReadBoundedShape(raw, &corpus, extraction.MaxEvaluationBytes) != nil {
		return errors.New("invalid or ambiguous extraction corpus JSON")
	}
	result, err := extraction.Evaluate(ctx, corpus)
	if err != nil {
		return err
	}
	return emit(out, result)
}
