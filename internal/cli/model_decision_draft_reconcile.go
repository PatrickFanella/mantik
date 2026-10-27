package cli

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"reflect"

	"git.subcult.tv/subculture-collective/mantik/internal/paperaccount"
	"git.subcult.tv/subculture-collective/mantik/internal/storage"
	"git.subcult.tv/subculture-collective/mantik/internal/workerplan"
)

// Reconciliation inspects the actual review owner. It never resumes acquisition,
// submits the retained command, or rewrites the original attempt directory.
func runModelDecisionDraftReconcile(ctx context.Context, args []string, out, errOut io.Writer) error {
	fs := flag.NewFlagSet("paper-decision-draft-reconcile", flag.ContinueOnError)
	fs.SetOutput(errOut)
	base := fs.String("api-url", defaultURL, "original Mantik service origin")
	receiver := fs.String("receiver-id", "", "expected actual service receiver")
	id := fs.String("decision-id", "", "original finalized decision ID")
	pin := fs.String("decision-sha256", "", "original finalized record hash")
	reviewID := fs.String("review-id", "", "original immutable typed review ID")
	capture := fs.String("capture-dir", "", "existing private attempt directory")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	u, err := url.Parse(*base)
	if err != nil || u.Scheme != "http" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || u.Path != "" && u.Path != "/" || fs.NArg() != 0 || !storage.ValidReceiverID(*receiver) || !paperaccount.ID.MatchString(*id) || !paperStrategyPin.MatchString(*pin) || !paperaccount.ID.MatchString(*reviewID) || !filepath.IsAbs(*capture) {
		return errors.New("reconciliation requires the original service, receiver, decision pins, review ID and absolute capture directory")
	}
	root, err := os.OpenRoot(*capture)
	if err != nil {
		return err
	}
	defer func() { _ = root.Close() }()
	read := func(name string, v any, limit int) error {
		f, err := root.Open(name)
		if err != nil {
			return err
		}
		info, statErr := f.Stat()
		if statErr != nil || !info.Mode().IsRegular() {
			_ = f.Close()
			return errors.New("draft artifact must be a regular file")
		}
		raw, readErr := io.ReadAll(io.LimitReader(f, int64(limit)+1))
		closeErr := f.Close()
		if err := errors.Join(readErr, closeErr); err != nil {
			return err
		}
		return workerplan.ReadBoundedShape(raw, v, limit)
	}
	var source struct {
		APIURL      string                      `json:"api_url"`
		Decision    storage.PaperDecisionRecord `json:"decision"`
		Explanation json.RawMessage             `json:"explanation"`
		ReviewID    string                      `json:"review_id"`
		Authority   string                      `json:"authority"`
	}
	if err := read("source.json", &source, 131072); err != nil {
		return err
	}
	var command storage.DecisionReviewCommand
	if err := read("review-intent.json", &command, storage.MaxDecisionReviewCommandBytes); err != nil {
		return err
	}
	d := source.Decision
	if source.APIURL != *base || source.Authority != "retained_owner_read_for_advisory_model_only" || source.ReviewID != *reviewID || d.Validate() != nil || d.Command.ID != *id || d.Command.ReceiverID != *receiver || d.SHA256 != *pin || command.Validate() != nil || command.Source != nil || command.ID != *reviewID || command.ReceiverID != *receiver || command.DecisionID != *id || command.DecisionSHA256 != *pin {
		return errors.New("retained draft review intent does not match the selected original source")
	}
	var actual storage.PaperDecisionRecord
	if err := requestBounded(ctx, *base, "/v1/paper-decisions/"+*id, nil, &actual, nil, 32768, true); err != nil {
		return err
	}
	if !reflect.DeepEqual(actual, d) {
		return errDecisionReadback
	}
	if _, err := fetchDecisionInspection(ctx, *base, actual, "e"); err != nil {
		return err
	}
	var retained storage.DecisionReviewRecord
	if err := requestBounded(ctx, *base, "/v1/paper-decision-reviews/"+*reviewID, nil, &retained, nil, storage.MaxDecisionReviewBytes, true); err != nil {
		var failure *httpFailure
		if errors.As(err, &failure) && failure.Status == http.StatusNotFound {
			return emit(out, map[string]any{"schema": "mantik-model-decision-draft-reconciliation-v1", "status": "not_found_at_read", "review_id": *reviewID, "decision_sha256": *pin, "authority": "service_review_readback_only", "retry_authorized": false, "qualification_credit": false})
		}
		return err
	}
	if !draftReviewMatches(retained, command, actual) {
		return errors.New("actual review owner differs from the retained draft review intent")
	}
	return emit(out, map[string]any{"schema": "mantik-model-decision-draft-reconciliation-v1", "status": "retained_review_verified", "review": retained, "authority": "service_review_readback_only", "model_execution_attested": false, "prose_verified": false, "retry_authorized": false, "qualification_credit": false})
}
