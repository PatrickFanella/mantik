package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/storage"
	"git.subcult.tv/subculture-collective/mantik/internal/workerplan"
)

const (
	automaticReviewPrefix    = "automatic-review-"
	maxResearchAttempts      = 3
	maxResearchCatalogBytes  = 8 << 20
	defaultResearchIntervalS = 60
)

func automaticReviewID(d storage.PaperDecisionRecord) string {
	return automaticReviewPrefix + workerplan.Hash(d.SHA256)
}

// runPaperResearchLoop schedules the advisory model review of each finalized
// entry candidate, at most one model request per interval. A model outage
// leaves a retained failed attempt and costs only that review: decisions,
// allocation and protective exits run in the service and never wait on this
// process.
func runPaperResearchLoop(ctx context.Context, args []string, out, errOut io.Writer) error {
	fs := flag.NewFlagSet("paper-research-loop", flag.ContinueOnError)
	fs.SetOutput(errOut)
	base := fs.String("api-url", defaultURL, "actual Mantik service origin")
	receiver := fs.String("receiver-id", "", "expected actual service receiver")
	root := fs.String("capture-root", "", "absolute private directory for attempt captures")
	config := fs.String("model-config", "", "explicit bounded private Ollama-routed model config JSON")
	interval := fs.Int("interval-seconds", defaultResearchIntervalS, "seconds between cycles, 1–3600; each cycle makes at most one model request")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if !filepath.IsAbs(*root) || *config == "" || !storage.ValidReceiverID(*receiver) || *interval < 1 || *interval > 3600 || fs.NArg() != 0 {
		return errors.New("research loop requires a receiver, an absolute capture root, a model config and a 1–3600 second interval")
	}
	if err := os.MkdirAll(*root, 0700); err != nil {
		return err
	}
	for {
		if err := researchCycle(ctx, *base, *receiver, *root, *config, out, errOut); err != nil && ctx.Err() == nil {
			slog.Error("paper research cycle failed", "error", err)
		}
		if waitContext(ctx, time.Duration(*interval)*time.Second) != nil {
			return nil
		}
	}
}

// researchCycle attempts the first entry candidate that has no retained review
// and has attempts left. Attempt directories are never reused, so they double
// as the attempt count across restarts.
func researchCycle(ctx context.Context, base, receiver, root, config string, out, errOut io.Writer) error {
	var catalog storage.PaperDecisionCatalog
	if err := requestBounded(ctx, base, "/v1/paper-decisions", nil, &catalog, nil, maxResearchCatalogBytes, true); err != nil {
		return err
	}
	if catalog.ReceiverID != receiver {
		return storage.ErrReceiverMismatch
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		return err
	}
	for _, d := range catalog.Records {
		if d.Validate() != nil {
			return storage.ErrPaperDecisionInvalid
		}
		if d.Evaluation.Action != "entry_candidate" {
			continue
		}
		id := automaticReviewID(d)
		attempts := 0
		for _, entry := range entries {
			if strings.HasPrefix(entry.Name(), id+"-") {
				attempts++
			}
		}
		if attempts >= maxResearchAttempts {
			continue
		}
		var retained storage.DecisionReviewRecord
		err := requestBounded(ctx, base, "/v1/paper-decision-reviews/"+id, nil, &retained, nil, storage.MaxDecisionReviewBytes, true)
		if err == nil {
			continue
		}
		var failure *httpFailure
		if !errors.As(err, &failure) || failure.Status != http.StatusNotFound {
			return err
		}
		capture := filepath.Join(root, fmt.Sprintf("%s-%d", id, attempts+1))
		return runModelDecisionDraft(ctx, []string{"--api-url", base, "--receiver-id", receiver, "--decision-id", d.Command.ID, "--decision-sha256", d.SHA256, "--review-id", id, "--capture-dir", capture, "--model-config", config}, out, errOut)
	}
	return nil
}
