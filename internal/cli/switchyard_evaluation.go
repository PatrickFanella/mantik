package cli

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"git.subcult.tv/subculture-collective/mantik/internal/extraction"
	"git.subcult.tv/subculture-collective/mantik/internal/workerplan"
	"io"
	"path/filepath"
)

const switchyardEvaluationManifestSchema = "mantik-switchyard-extraction-evaluation-manifest-v1"

type switchyardEvaluationManifest struct {
	Schema string                             `json:"schema"`
	Cases  []switchyardEvaluationManifestCase `json:"cases"`
}
type switchyardEvaluationManifestCase struct {
	ID         string              `json:"id"`
	CaptureDir string              `json:"capture_dir"`
	Expected   extraction.Response `json:"expected"`
}

func runSwitchyardEvaluation(ctx context.Context, args []string, out, errOut io.Writer) error {
	fs := flag.NewFlagSet("news-switchyard-evaluate", flag.ContinueOnError)
	fs.SetOutput(errOut)
	path := fs.String("manifest", "", "absolute private manifest with capture directories and expected responses")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if fs.NArg() != 0 || !filepath.IsAbs(*path) || filepath.Clean(*path) != *path {
		return extraction.ErrInvalid
	}
	root, err := openPrivateLocalRoot(filepath.Dir(*path))
	if err != nil {
		return err
	}
	defer root.Close()
	raw, err := readPrivateLocalFile(root, filepath.Base(*path), extraction.MaxEvaluationBytes)
	if err != nil {
		return extraction.ErrInvalid
	}
	var manifest switchyardEvaluationManifest
	if workerplan.ReadBoundedShape(raw, &manifest, extraction.MaxEvaluationBytes) != nil || manifest.Schema != switchyardEvaluationManifestSchema || len(manifest.Cases) < 1 || len(manifest.Cases) > extraction.MaxEvaluationCases {
		return extraction.ErrInvalid
	}
	corpus := extraction.SwitchyardCorpus{Schema: extraction.SwitchyardCorpusSchema, Cases: []extraction.SwitchyardEvaluationInput{}}
	total := 0
	for _, item := range manifest.Cases {
		if err := ctx.Err(); err != nil {
			return err
		}
		p, f, err := loadSwitchyardReceipts(item.CaptureDir)
		if err != nil {
			return err
		}
		entry := extraction.SwitchyardEvaluationInput{ID: item.ID, Prepared: p, Finished: f, Expected: item.Expected}
		encoded, e := json.Marshal(entry)
		if e != nil {
			return extraction.ErrInvalid
		}
		total += len(encoded) + 1
		if total > extraction.MaxSwitchyardEvaluationBytes-128 {
			return extraction.ErrInvalid
		}
		corpus.Cases = append(corpus.Cases, entry)
	}
	result, err := extraction.EvaluateSwitchyard(ctx, corpus)
	if err != nil {
		return err
	}
	return emit(out, result)
}
