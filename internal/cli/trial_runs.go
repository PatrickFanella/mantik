package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/url"

	"git.subcult.tv/subculture-collective/mantik/internal/assessment"
	"git.subcult.tv/subculture-collective/mantik/internal/news"
	"git.subcult.tv/subculture-collective/mantik/internal/review"
	"git.subcult.tv/subculture-collective/mantik/internal/storage"
	"git.subcult.tv/subculture-collective/mantik/internal/trial"
)

func runTrialExecution(ctx context.Context, command string, args []string, out, errOut io.Writer) error {
	fs := flag.NewFlagSet(command, flag.ContinueOnError)
	fs.SetOutput(errOut)
	base := fs.String("api-url", defaultURL, "service HTTP origin")
	id := fs.String("id", "", "retained attempt intent ID")
	var receiver, artifact string
	if command == "trial-run-execute" || command == "trial-run-resolve" {
		fs.StringVar(&receiver, "receiver-id", "", "explicit pinned research receiver identity")
	}
	if command == "trial-run" {
		fs.StringVar(&artifact, "artifact", "", "inspect input or result artifact instead of run metadata")
	}
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	u, err := url.Parse(*base)
	if err != nil || u.Scheme != "http" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Path != "" && u.Path != "/" || fs.NArg() != 0 || !review.ID.MatchString(*id) {
		return trial.ErrInvalid
	}
	path := "/v1/trial-attempts/" + *id + "/run"
	if command == "trial-assessments" {
		var history assessment.History
		if err = request(ctx, *base, "/v1/hypothesis-trials/"+*id+"/assessments", nil, &history); err != nil {
			return err
		}
		if history.Validate() != nil || history.Index.TrialID != *id {
			return errors.New("invalid assessment history receipt")
		}
		return emit(out, history)
	}
	if command == "trial-assessment" {
		var result assessment.Result
		if err = request(ctx, *base, "/v1/trial-attempts/"+*id+"/assessment", nil, &result); err != nil {
			return err
		}
		hash := result.SHA256
		result.SHA256 = ""
		if result.Schema != assessment.Schema || result.AttemptID != *id || result.Authority != "synthetic_condition_comparison_only" || result.QualificationCredit || hash != trial.Hash(result) {
			return errors.New("invalid assessment receipt")
		}
		result.SHA256 = hash
		return emit(out, result)
	}
	var body any
	if command != "trial-run" {
		if !storage.ValidReceiverID(receiver) {
			return trial.ErrInvalid
		}
		body = struct {
			ReceiverID string `json:"receiver_id"`
		}{receiver}
		if command == "trial-run-resolve" {
			path += "/resolve"
		}
	}
	if artifact != "" {
		if artifact != "input" && artifact != "result" {
			return trial.ErrInvalid
		}
		var a trial.RunArtifact
		if err = requestWithQuery(ctx, *base, path+"/"+artifact, nil, &a, nil, 32<<20); err != nil {
			return err
		}
		if a.AttemptID != *id || a.Kind != artifact || news.SHA256(a.Content) != a.SHA256 {
			return errors.New("invalid run artifact digest")
		}
		return emit(out, a)
	}
	var run trial.Run
	if err = request(ctx, *base, path, body, &run); err != nil {
		return err
	}
	if run.AttemptID != *id || run.Validate() != nil || receiver != "" && receiver != run.ReceiverID {
		return errors.New("run receipt conflicts with request")
	}
	if err = emit(out, run); err != nil {
		return err
	}
	if command == "trial-run-execute" && run.Status != "completed" {
		return fmt.Errorf("native trial %s: %s (run retained)", run.Status, run.ErrorCode)
	}
	return nil
}
