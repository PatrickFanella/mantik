package cli

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"io"
	"os"
	"syscall"

	"git.subcult.tv/subculture-collective/mantik/internal/extraction"
	"git.subcult.tv/subculture-collective/mantik/internal/news"
)

type recordedExtractor []byte

func (r recordedExtractor) Extract(ctx context.Context, _ extraction.Frame) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return []byte(r), nil
}

func readExtractionFile(path string, max int64) ([]byte, error) {
	// A FIFO must not block before the regular-file check can run.
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, errors.New("cannot open local extraction input")
	}
	info, statErr := f.Stat()
	if statErr != nil || !info.Mode().IsRegular() || info.Size() > max {
		_ = f.Close()
		return nil, errors.New("extraction input requires a bounded regular file")
	}
	raw, readErr := io.ReadAll(io.LimitReader(f, max+1))
	if err := errors.Join(readErr, f.Close()); err != nil || len(raw) > int(max) {
		return nil, errors.New("cannot read bounded extraction input")
	}
	return raw, nil
}

func runNewsExtract(ctx context.Context, args []string, out, errOut io.Writer) error {
	fs := flag.NewFlagSet("news-extract", flag.ContinueOnError)
	fs.SetOutput(errOut)
	article := fs.String("article", "", "local source-only article JSON")
	model := fs.String("model-fixture", "", "one raw recorded extractor response; no provider calls")
	key := fs.String("event-key", "", "operator-selected event key, not inferred corroboration")
	contract := fs.Bool("export-contract", false, "print the bounded extraction contract")
	sourceFixture := fs.Bool("export-article-fixture", false, "print an invented source-only article")
	modelFixture := fs.Bool("export-model-fixture", false, "print invented extractor output for the article fixture")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	exports := 0
	for _, enabled := range []bool{*contract, *sourceFixture, *modelFixture} {
		if enabled {
			exports++
		}
	}
	if fs.NArg() != 0 || exports > 1 || exports == 1 && (*article != "" || *model != "" || *key != "") {
		return errors.New("invalid news-extract arguments")
	}
	if *contract {
		return emit(out, extraction.Definition())
	}
	if *sourceFixture {
		return emit(out, news.FixtureArticle())
	}
	if *modelFixture {
		return emit(out, extraction.FixtureResponse())
	}
	if *article == "" || *model == "" || *key == "" {
		return errors.New("news-extract requires article, model-fixture and event-key")
	}
	raw, err := readExtractionFile(*article, news.MaxRecordingBytes)
	if err != nil {
		return err
	}
	a, err := news.ReadArticle(bytes.NewReader(raw))
	if err != nil {
		return err
	}
	artifact, err := readExtractionFile(*model, extraction.MaxArtifactBytes)
	if err != nil {
		return err
	}
	result, err := extraction.Run(ctx, a, *key, recordedExtractor(artifact))
	if err != nil {
		return err
	}
	return emit(out, struct {
		Mode   string            `json:"mode"`
		Result extraction.Result `json:"result"`
	}{Mode: "recorded_model_fixture", Result: result})
}
