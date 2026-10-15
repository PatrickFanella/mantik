package cli

import (
	"context"
	"errors"
	"flag"
	"io"
	"net/url"

	"git.subcult.tv/subculture-collective/mantik/internal/review"
	"git.subcult.tv/subculture-collective/mantik/internal/storage"
	"git.subcult.tv/subculture-collective/mantik/internal/trial"
)

func runTrialAttempt(ctx context.Context, command string, args []string, out, errOut io.Writer) error {
	fs := flag.NewFlagSet(command, flag.ContinueOnError)
	fs.SetOutput(errOut)
	base := fs.String("api-url", defaultURL, "service HTTP origin")
	id := fs.String("id", "", "attempt ID, or registered trial ID for attempt history")
	var receiver, trialID string
	if command == "trial-attempt-start" || command == "trial-attempt-cancel" {
		fs.StringVar(&receiver, "receiver-id", "", "explicit research receiver identity")
	}
	if command == "trial-attempt-start" {
		fs.StringVar(&trialID, "trial", "", "registered trial ID")
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
	if command == "trial-attempts" {
		var h trial.AttemptHistory
		if err = request(ctx, *base, "/v1/hypothesis-trials/"+*id+"/attempts", nil, &h); err != nil {
			return err
		}
		if h.Schema != trial.AttemptsSchema || h.TrialID != *id {
			return errors.New("attempt history conflicts with request")
		}
		for _, a := range h.Attempts {
			if a.TrialID != *id || a.Validate() != nil {
				return errors.New("invalid attempt history")
			}
		}
		return emit(out, h)
	}
	path := "/v1/trial-attempts/" + *id
	var body any
	switch command {
	case "trial-attempt-start":
		if !storage.ValidReceiverID(receiver) || !review.ID.MatchString(trialID) {
			return trial.ErrInvalid
		}
		path = "/v1/trial-attempts"
		body = trial.AttemptCommand{ReceiverID: receiver, RequestID: *id, TrialID: trialID}
	case "trial-attempt-cancel":
		if !storage.ValidReceiverID(receiver) {
			return trial.ErrInvalid
		}
		path += "/cancel"
		body = struct {
			ReceiverID string `json:"receiver_id"`
		}{receiver}
	}
	var a trial.Attempt
	if err = request(ctx, *base, path, body, &a); err != nil {
		return err
	}
	if a.ID != *id || a.Validate() != nil || receiver != "" && a.ReceiverID != receiver || trialID != "" && a.TrialID != trialID || command == "trial-attempt-cancel" && a.Status != "cancelled" {
		return errors.New("attempt receipt conflicts with request")
	}
	return emit(out, a)
}
