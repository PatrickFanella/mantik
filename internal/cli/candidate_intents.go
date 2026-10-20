package cli

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"io"
	"net/url"

	"git.subcult.tv/subculture-collective/mantik/internal/candidateattempt"
	"git.subcult.tv/subculture-collective/mantik/internal/candidateexec"
	"git.subcult.tv/subculture-collective/mantik/internal/review"
	"git.subcult.tv/subculture-collective/mantik/internal/storage"
	"git.subcult.tv/subculture-collective/mantik/internal/workerplan"
)

func runCandidateIntents(ctx context.Context, command string, args []string, out, errOut io.Writer) error {
	fs := flag.NewFlagSet(command, flag.ContinueOnError)
	fs.SetOutput(errOut)
	base := fs.String("api-url", defaultURL, "service HTTP origin")
	receiverID := fs.String("receiver-id", "", "required expected research receiver identity")
	var id, registration, registrationSHA, file, jobSHA, intentSHA, artifact string
	if command != "candidate-worker-intents" {
		fs.StringVar(&id, "id", "", "intent request ID")
	}
	if command == "candidate-worker-intent-start" {
		fs.StringVar(&registration, "registration", "", "retained candidate registration ID")
		fs.StringVar(&registrationSHA, "registration-sha256", "", "required expected registration identity")
		fs.StringVar(&file, "job", "", "sealed synthetic behavior job file; no execution")
		fs.StringVar(&jobSHA, "job-sha256", "", "required expected behavior job identity")
	}
	if command == "candidate-worker-intent" {
		fs.StringVar(&intentSHA, "intent-sha256", "", "required expected retained intent identity")
		fs.StringVar(&artifact, "artifact", "intent", "intent receipt or pinned job; neither executes code")
	}
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	u, err := url.Parse(*base)
	if err != nil || u.Scheme != "http" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Path != "" && u.Path != "/" || fs.NArg() != 0 || !storage.ValidReceiverID(*receiverID) {
		return errors.New("candidate intent commands require an HTTP origin, explicit receiver-id and no positional arguments")
	}
	if command == "candidate-worker-intents" {
		var h candidateattempt.History
		if err := candidateRequest(ctx, *base, "/v1/candidate-worker-intents", nil, &h, candidateattempt.MaxHistoryBytes); err != nil {
			return err
		}
		if h.Validate() != nil || h.ReceiverID != *receiverID {
			return candidateattempt.ErrInvalid
		}
		return emit(out, h)
	}
	if !review.ID.MatchString(id) {
		return candidateattempt.ErrInvalid
	}
	if command == "candidate-worker-intent" {
		if !candidateRegistrySHA.MatchString(intentSHA) || artifact != "intent" && artifact != "job" {
			return candidateattempt.ErrInvalid
		}
		var i candidateattempt.Intent
		if err := candidateRequest(ctx, *base, "/v1/candidate-worker-intents/"+id, nil, &i, candidateattempt.MaxBytes); err != nil {
			return err
		}
		if i.Validate() != nil || i.Command.RequestID != id || i.Command.ReceiverID != *receiverID || i.SHA256 != intentSHA {
			return candidateattempt.ErrInvalid
		}
		if artifact == "job" {
			return emit(out, i.Command.Job)
		}
		return emit(out, i)
	}
	if !review.ID.MatchString(registration) || !candidateRegistrySHA.MatchString(registrationSHA) || !candidateRegistrySHA.MatchString(jobSHA) || file == "" {
		return candidateattempt.ErrInvalid
	}
	raw, err := externalFile(file, candidateexec.MaxJobBytes)
	if err != nil {
		return err
	}
	j, err := candidateexec.ReadJob(bytes.NewReader(raw))
	if err != nil || j.SHA256 != jobSHA {
		return candidateattempt.ErrInvalid
	}
	c := candidateattempt.Command{ReceiverID: *receiverID, RequestID: id, RegistrationID: registration, RegistrationSHA256: registrationSHA, Job: j}
	if c.Validate() != nil {
		return candidateattempt.ErrInvalid
	}
	var i candidateattempt.Intent
	if err := candidateRequest(ctx, *base, "/v1/candidate-worker-intents", c, &i, candidateattempt.MaxBytes); err != nil {
		return err
	}
	if i.Validate() != nil || workerplan.Hash(i.Command) != workerplan.Hash(c) {
		return candidateattempt.ErrInvalid
	}
	return emit(out, i)
}
