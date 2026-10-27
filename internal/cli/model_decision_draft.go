package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/decisiondraft"
	"git.subcult.tv/subculture-collective/mantik/internal/paperaccount"
	"git.subcult.tv/subculture-collective/mantik/internal/storage"
	"git.subcult.tv/subculture-collective/mantik/internal/workerplan"
)

// A new private capture directory is an acquisition identity. It is never
// reopened for another model request after an interrupted or failed attempt.
func draftArtifact(root *os.Root, name string, value any) error {
	raw, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	f, err := root.OpenFile(name, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	_, writeErr := f.Write(append(raw, '\n'))
	syncErr := f.Sync()
	closeErr := f.Close()
	if err := errors.Join(writeErr, syncErr, closeErr); err != nil {
		return err
	}
	dir, err := root.Open(".")
	if err != nil {
		return err
	}
	return errors.Join(dir.Sync(), dir.Close())
}
func runModelDecisionDraft(ctx context.Context, args []string, out, errOut io.Writer) error {
	fs := flag.NewFlagSet("paper-decision-draft", flag.ContinueOnError)
	fs.SetOutput(errOut)
	base := fs.String("api-url", defaultURL, "actual Mantik service origin")
	receiver := fs.String("receiver-id", "", "expected actual service receiver")
	id := fs.String("decision-id", "", "existing finalized decision ID")
	pin := fs.String("decision-sha256", "", "expected actual finalized record hash")
	reviewID := fs.String("review-id", "", "immutable typed review request ID")
	capture := fs.String("capture-dir", "", "new absolute private attempt directory")
	configPath := fs.String("model-config", "", "explicit bounded private Ollama-routed model config JSON")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	u, err := url.Parse(*base)
	if err != nil || u.Scheme != "http" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || u.Path != "" && u.Path != "/" || fs.NArg() != 0 || !storage.ValidReceiverID(*receiver) || !paperaccount.ID.MatchString(*id) || !paperStrategyPin.MatchString(*pin) || !paperaccount.ID.MatchString(*reviewID) || !filepath.IsAbs(*capture) || *configPath == "" {
		return errors.New("draft requires actual source pins, explicit model config and a new absolute capture directory")
	}
	f, err := os.Open(*configPath)
	if err != nil {
		return err
	}
	raw, readErr := io.ReadAll(io.LimitReader(f, 4097))
	closeErr := f.Close()
	if err := errors.Join(readErr, closeErr); err != nil {
		return err
	}
	if len(raw) > 4096 {
		return errors.New("model config exceeds bound")
	}
	var config decisiondraft.Config
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&config) != nil || decoder.Decode(new(any)) != io.EOF || config.Validate() != nil {
		return errors.New("invalid model config")
	}
	var d storage.PaperDecisionRecord
	if err := requestBounded(ctx, *base, "/v1/paper-decisions/"+*id, nil, &d, nil, 32768, true); err != nil {
		return err
	}
	if d.Validate() != nil || d.Command.ReceiverID != *receiver || d.Command.ID != *id || d.SHA256 != *pin {
		return errDecisionReadback
	}
	// Read and independently bind the deterministic owner explanation too. The
	// model receives only this finalized decision, never arbitrary caller prose.
	explanation, err := fetchDecisionInspection(ctx, *base, d, "e")
	if err != nil {
		return err
	}
	var prior storage.DecisionReviewRecord
	priorErr := requestBounded(ctx, *base, "/v1/paper-decision-reviews/"+*reviewID, nil, &prior, nil, storage.MaxDecisionReviewBytes, true)
	if priorErr == nil {
		return errors.New("review ID already retained; inspect its original receipt instead of issuing another model request")
	}
	var failure *httpFailure
	if !errors.As(priorErr, &failure) || failure.Status != http.StatusNotFound {
		return priorErr
	}
	if err := os.Mkdir(*capture, 0700); err != nil {
		return errors.New("capture directory must be new; inspect prior attempt and service review before retry")
	}
	root, err := os.OpenRoot(*capture)
	if err != nil {
		return err
	}
	defer func() { _ = root.Close() }()
	if err := draftArtifact(root, "source.json", map[string]any{"api_url": *base, "decision": d, "explanation": explanation, "review_id": *reviewID, "authority": "retained_owner_read_for_advisory_model_only"}); err != nil {
		return err
	}
	number := 0
	receipt, err := decisiondraft.Generate(ctx, config, d, os.Getenv("MANTIK_MODEL_TOKEN"), func(r decisiondraft.Receipt) error {
		number++
		return draftArtifact(root, fmt.Sprintf("model-%d.json", number), r)
	})
	if err != nil {
		_ = emit(out, map[string]any{"status": receipt.Status, "capture_dir": *capture, "review_submitted": false})
		return err
	}
	command := storage.DecisionReviewCommand{ID: *reviewID, ReceiverID: *receiver, DecisionID: *id, DecisionSHA256: *pin, Explanation: receipt.Draft.Explanation, Claims: receipt.Draft.Claims}
	commandRaw, _ := json.Marshal(command)
	if command.Validate() != nil || len(commandRaw) > storage.MaxDecisionReviewCommandBytes {
		return storage.ErrDecisionReviewInvalid
	}
	if err := draftArtifact(root, "review-intent.json", command); err != nil {
		return err
	}
	var reviewed storage.DecisionReviewReceipt
	// This is the same shared application operation exposed by supervision/MCP.
	if err := requestBounded(ctx, *base, "/v1/paper-decision-reviews", command, &reviewed, nil, storage.MaxDecisionReviewBytes+4096, true); err != nil {
		if keep := draftArtifact(root, "review-outcome.json", map[string]string{"status": "outcome_unverified", "review_id": *reviewID}); keep != nil {
			return keep
		}
		return errors.New("review outcome unverified; inspect actual review owner before retrying the retained command")
	}
	if err := draftArtifact(root, "review-response.json", reviewed); err != nil {
		return err
	}
	if !draftReviewMatches(reviewed.Record, command, d) {
		return errors.New("model draft review readback does not match actual source")
	}
	// Read the retained owner record after POST; a lost read remains inspectable
	// in review-response.json and must not trigger another model request.
	var retained storage.DecisionReviewRecord
	if err := requestBounded(ctx, *base, "/v1/paper-decision-reviews/"+*reviewID, nil, &retained, nil, storage.MaxDecisionReviewBytes, true); err != nil {
		return err
	}
	if !reflect.DeepEqual(retained, reviewed.Record) {
		return errors.New("retained review differs from returned receipt")
	}
	if err := draftArtifact(root, "complete.json", map[string]any{"at": time.Now().UTC(), "review": reviewed, "model_receipt_sha256": receipt.SHA256, "prose_verified": false, "authority": "advisory_model_draft_and_retained_typed_review_only"}); err != nil {
		return err
	}
	return emit(out, map[string]any{"schema": "mantik-model-decision-draft-result-v1", "capture_dir": *capture, "review": reviewed, "model_request_completed": true, "prose_verified": false, "qualification_credit": false})
}

func draftReviewMatches(r storage.DecisionReviewRecord, command storage.DecisionReviewCommand, d storage.PaperDecisionRecord) bool {
	hash := r.SHA256
	r.SHA256 = ""
	return reflect.DeepEqual(r.Command, command) && hash == workerplan.Hash(r) && r.Schema == "mantik-finalized-decision-review-v1" && r.Method == "retained-decision-fact-claims-v1" && r.Authority == "finalized_decision_claim_review_only" && !r.ProseVerified && !r.ModelUsed && !r.AdmissionAvailable && !r.ExecutionAvailable && !r.QualificationCredit && r.BasisSHA256 == d.BasisSHA256 && !r.ReviewedAt.Before(d.FinalizedAt) && r.RecordPath == "/v1/paper-decisions/"+d.Command.ID && (r.Status == "claims_consistent" && r.CaseID == "" || r.Status == "review_required" && r.CaseID == "decision-review-"+d.SHA256)
}
