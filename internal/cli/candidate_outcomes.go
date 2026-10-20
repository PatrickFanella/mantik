package cli

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"git.subcult.tv/subculture-collective/mantik/internal/candidateevidence"
	"git.subcult.tv/subculture-collective/mantik/internal/review"
	"git.subcult.tv/subculture-collective/mantik/internal/storage"
	"io"
	"net/url"
)

func runCandidateOutcomes(ctx context.Context, command string, args []string, out, errOut io.Writer) error {
	fs := flag.NewFlagSet(command, flag.ContinueOnError)
	fs.SetOutput(errOut)
	base := fs.String("api-url", defaultURL, "service HTTP origin")
	receiver := fs.String("receiver-id", "", "required expected receiver identity")
	id := fs.String("id", "", "required intent request ID")
	intentSHA := fs.String("intent-sha256", "", "required expected service intent identity")
	captureSHA := fs.String("capture-sha256", "", "required expected capture identity")
	var file, outcomeSHA, artifact string
	if command == "candidate-worker-outcome-report" {
		fs.StringVar(&file, "capture", "", "portable caller-reported capture file")
	} else {
		fs.StringVar(&outcomeSHA, "outcome-sha256", "", "required expected retained outcome identity")
		fs.StringVar(&artifact, "artifact", "outcome", "outcome receipt, capture JSON or exact stdout|stderr bytes")
	}
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	u, err := url.Parse(*base)
	if err != nil || u.Scheme != "http" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Path != "" && u.Path != "/" || fs.NArg() != 0 || !storage.ValidReceiverID(*receiver) || !review.ID.MatchString(*id) || !candidateRegistrySHA.MatchString(*intentSHA) || !candidateRegistrySHA.MatchString(*captureSHA) {
		return candidateevidence.ErrConflict
	}
	matches := func(r candidateevidence.OutcomeRecord) bool {
		return r.Validate() == nil && r.ReceiverID == *receiver && r.RequestID == *id && r.IntentSHA256 == *intentSHA && r.CaptureSHA256 == *captureSHA
	}
	if command == "candidate-worker-outcome-report" {
		if file == "" {
			return candidateevidence.ErrConflict
		}
		raw, err := externalFile(file, candidateevidence.MaxBytes)
		if err != nil {
			return err
		}
		c, err := candidateevidence.Read(bytes.NewReader(raw))
		if err != nil {
			return err
		}
		report := candidateevidence.Report{ReceiverID: *receiver, RequestID: *id, IntentSHA256: *intentSHA, Capture: c}
		if report.Validate() != nil || c.SHA256 != *captureSHA {
			return candidateevidence.ErrConflict
		}
		var r candidateevidence.OutcomeRecord
		if err := candidateRequest(ctx, *base, "/v1/candidate-worker-outcomes", report, &r, candidateevidence.MaxOutcomeBytes); err != nil {
			return err
		}
		if !matches(r) || r.VerifyCapture(*c.Attempt.Intent.ServiceIntent, c) != nil {
			return candidateevidence.ErrConflict
		}
		return emit(out, r)
	}
	if !candidateRegistrySHA.MatchString(outcomeSHA) || artifact != "outcome" && artifact != "capture" && artifact != "stdout" && artifact != "stderr" {
		return candidateevidence.ErrConflict
	}
	path := "/v1/candidate-worker-outcomes/" + *id
	if artifact == "outcome" {
		var r candidateevidence.OutcomeRecord
		if err := candidateRequest(ctx, *base, path, nil, &r, candidateevidence.MaxOutcomeBytes); err != nil {
			return err
		}
		if !matches(r) || r.SHA256 != outcomeSHA {
			return candidateevidence.ErrConflict
		}
		return emit(out, r)
	}
	var a candidateevidence.Artifact
	if err := candidateRequest(ctx, *base, path+"/capture", nil, &a, candidateevidence.MaxArtifactBytes); err != nil {
		return err
	}
	if a.Validate() != nil || !matches(a.Outcome) || a.Outcome.SHA256 != outcomeSHA {
		return candidateevidence.ErrConflict
	}
	switch artifact {
	case "stdout":
		_, err = io.Copy(out, bytes.NewReader(a.Capture.Stdout))
	case "stderr":
		_, err = io.Copy(out, bytes.NewReader(a.Capture.Stderr))
	default:
		return emit(out, a.Capture)
	}
	return err
}
