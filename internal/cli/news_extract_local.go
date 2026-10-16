package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"io"
	"os"
	"path/filepath"

	"git.subcult.tv/subculture-collective/mantik/internal/extraction"
	"git.subcult.tv/subculture-collective/mantik/internal/news"
)

func retainLocalReceipt(dir string, r extraction.LocalReceipt) error {
	name := "prepared.json"
	if r.Status == "finished" {
		name = "finished.json"
	}
	raw, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return err
	}
	f, err := os.OpenFile(filepath.Join(dir, name), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	_, writeErr := f.Write(append(raw, '\n'))
	err = errors.Join(writeErr, f.Sync(), f.Close())
	if err != nil {
		return err
	}
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	return errors.Join(d.Sync(), d.Close())
}

func runNewsExtractLocal(ctx context.Context, args []string, out, errOut io.Writer) error {
	fs := flag.NewFlagSet("news-extract-local", flag.ContinueOnError)
	fs.SetOutput(errOut)
	article := fs.String("article", "", "local source-only article JSON")
	endpoint := fs.String("endpoint", "", "private literal IP origin for Switchyard-compatible chat")
	model := fs.String("model", "", "explicit ollama/MODEL; no cloud models")
	key := fs.String("event-key", "", "operator-declared event key")
	tokens := fs.Int("max-tokens", 1024, "maximum generated tokens, 64–4096")
	dir := fs.String("output-dir", "", "new absolute private receipt directory; never reused")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	config := extraction.LocalConfig{Endpoint: *endpoint, Model: *model, MaxTokens: *tokens}
	if fs.NArg() != 0 || *article == "" || *key == "" || !filepath.IsAbs(*dir) || filepath.Clean(*dir) != *dir || config.Validate() != nil {
		return errors.New("news-extract-local requires article, event-key, local endpoint, explicit model and new absolute output-dir")
	}
	raw, err := readExtractionFile(*article, news.MaxRecordingBytes)
	if err != nil {
		return err
	}
	a, err := news.ReadArticle(bytes.NewReader(raw))
	if err != nil {
		return err
	}
	// Exclusive creation prevents overwriting prior attempts or silently retrying
	// one whose response was lost. The directory remains evidence on any error.
	if err := os.Mkdir(*dir, 0700); err != nil {
		return errors.New("local extraction output directory must be new")
	}
	r, err := extraction.RunLocal(ctx, a, *key, config, os.Getenv("MANTIK_LOCAL_MODEL_TOKEN"), func(r extraction.LocalReceipt) error {
		return retainLocalReceipt(*dir, r)
	})
	if err != nil {
		return err
	}
	return emit(out, r)
}
