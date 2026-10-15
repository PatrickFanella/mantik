package cli

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"io"
	"net/url"
	"os"
	"regexp"

	"git.subcult.tv/subculture-collective/mantik/internal/review"
	"git.subcult.tv/subculture-collective/mantik/internal/storage"
)

func runNewsReview(ctx context.Context, command string, args []string, out, errOut io.Writer) error {
	fs := flag.NewFlagSet(command, flag.ContinueOnError)
	fs.SetOutput(errOut)
	base := fs.String("api-url", defaultURL, "service HTTP origin")
	id := fs.String("id", "", "stable review request/attempt ID")
	var receiver, caseID, file string
	var version int
	if command != "news-review" {
		fs.StringVar(&receiver, "receiver-id", "", "explicit pinned research receiver identity")
	}
	if command == "news-review-start" {
		fs.StringVar(&caseID, "case", "", "news research case ID")
		fs.IntVar(&version, "version", 0, "current case evidence version")
	}
	if command == "news-review-complete" {
		fs.StringVar(&file, "model-fixture", "", "private recorded model artifact array")
	}
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	u, err := url.Parse(*base)
	if err != nil || u.Scheme != "http" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Path != "" && u.Path != "/" {
		return errors.New("api-url must be an HTTP origin without credentials or a path")
	}
	if fs.NArg() != 0 || !review.ID.MatchString(*id) {
		return errors.New("review command requires a bounded explicit id")
	}
	var view review.View
	if command == "news-review" {
		if err = request(ctx, *base, "/v1/news-reviews/"+*id, nil, &view); err != nil {
			return err
		}
		return emit(out, view)
	}
	if !storage.ValidReceiverID(receiver) {
		return errors.New("review writes require an explicit receiver-id")
	}
	if command == "news-review-start" {
		if version < 1 || !regexp.MustCompile(`^news-case-[a-f0-9]{64}$`).MatchString(caseID) {
			return errors.New("review start requires a news case and current evidence version")
		}
		body := struct {
			ReceiverID string `json:"receiver_id"`
			RequestID  string `json:"request_id"`
			CaseID     string `json:"case_id"`
			Version    int    `json:"version"`
		}{receiver, *id, caseID, version}
		if err = request(ctx, *base, "/v1/news-reviews", body, &view); err != nil {
			return err
		}
		if view.Attempt.ID != *id || view.Attempt.Pin.Subject.ReceiverID != receiver || view.Attempt.Pin.Subject.CaseID != caseID || view.Attempt.Pin.Subject.CaseVersion != version {
			return errors.New("review start receipt conflicts with pinned request")
		}
		return emit(out, view)
	}
	if command != "news-review-complete" || file == "" {
		return errors.New("review completion requires model-fixture")
	}
	f, err := os.Open(file)
	if err != nil {
		return errors.New("cannot open recorded model fixture")
	}
	raw, readErr := io.ReadAll(io.LimitReader(f, (128<<10)+1))
	if err = errors.Join(readErr, f.Close()); err != nil {
		return err
	}
	if len(raw) > 128<<10 {
		return errors.New("recorded model fixture exceeds bound")
	}
	var steps []json.RawMessage
	if err = json.Unmarshal(raw, &steps); err != nil {
		return errors.New("invalid recorded model artifact array")
	}
	steps, digest, err := review.CanonicalSteps(steps)
	if err != nil {
		return err
	}
	body := struct {
		ReceiverID string            `json:"receiver_id"`
		Steps      []json.RawMessage `json:"steps"`
	}{receiver, steps}
	if err = request(ctx, *base, "/v1/news-reviews/"+*id+"/complete", body, &view); err != nil {
		return err
	}
	if view.Attempt.ID != *id || view.Attempt.Pin.Subject.ReceiverID != receiver || view.Attempt.StepsSHA256 != digest {
		return errors.New("review completion receipt conflicts with recorded artifacts")
	}
	return emit(out, view)
}
