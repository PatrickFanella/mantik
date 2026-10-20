package cli

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"git.subcult.tv/subculture-collective/mantik/internal/candidategeneration"
	"git.subcult.tv/subculture-collective/mantik/internal/review"
	"git.subcult.tv/subculture-collective/mantik/internal/storage"
	"io"
	"net/url"
	"reflect"
)

func runGenerationOutcomes(ctx context.Context, command string, args []string, out, errOut io.Writer) error {
	fs := flag.NewFlagSet(command, flag.ContinueOnError)
	fs.SetOutput(errOut)
	base := fs.String("api-url", defaultURL, "service HTTP origin")
	receiver := fs.String("receiver-id", "", "required expected receiver identity")
	var id, intentSHA, planSHA, recordingSHA, outcomeSHA, file, artifact string
	if command != "candidate-generation-outcomes" {
		fs.StringVar(&id, "id", "", "required intent request ID")
		fs.StringVar(&intentSHA, "intent-sha256", "", "required expected service intent identity")
		fs.StringVar(&planSHA, "plan-sha256", "", "required expected full generation plan identity")
		fs.StringVar(&recordingSHA, "recording-sha256", "", "required expected exact recording identity")
		if command == "candidate-generation-outcome-report" {
			fs.StringVar(&file, "recording", "", "caller-recorded output file; no model call")
		} else {
			fs.StringVar(&outcomeSHA, "outcome-sha256", "", "required expected retained outcome identity")
			fs.StringVar(&artifact, "artifact", "outcome", "outcome, recording, exact output, candidate or exact source")
		}
	}
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	u, err := url.Parse(*base)
	if err != nil || u.Scheme != "http" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Path != "" && u.Path != "/" || fs.NArg() != 0 || !storage.ValidReceiverID(*receiver) {
		return candidategeneration.ErrInvalid
	}
	if command == "candidate-generation-outcomes" {
		var h candidategeneration.OutcomeHistory
		if err := candidateRequest(ctx, *base, "/v1/candidate-generation-outcomes", nil, &h, candidategeneration.MaxOutcomeHistoryBytes); err != nil {
			return err
		}
		if h.Validate() != nil || h.ReceiverID != *receiver {
			return candidategeneration.ErrInvalid
		}
		return emit(out, h)
	}
	if !review.ID.MatchString(id) || !candidateRegistrySHA.MatchString(intentSHA) || !candidateRegistrySHA.MatchString(planSHA) || !candidateRegistrySHA.MatchString(recordingSHA) {
		return candidategeneration.ErrInvalid
	}
	matches := func(o candidategeneration.Outcome) bool {
		return o.Validate() == nil && o.ReceiverID == *receiver && o.RequestID == id && o.IntentSHA256 == intentSHA && o.PlanSHA256 == planSHA && o.RecordingSHA256 == recordingSHA
	}
	if command == "candidate-generation-outcome-report" {
		if file == "" {
			return candidategeneration.ErrInvalid
		}
		raw, err := externalFile(file, candidategeneration.MaxRecordingBytes)
		if err != nil {
			return err
		}
		rec, err := candidategeneration.ReadRecording(bytes.NewReader(raw))
		if err != nil || rec.SHA256 != recordingSHA || rec.Plan.SHA256 != planSHA {
			return candidategeneration.ErrInvalid
		}
		r := candidategeneration.Report{Schema: candidategeneration.ReportSchema, ReceiverID: *receiver, RequestID: id, IntentSHA256: intentSHA, Recording: rec}
		if r.Validate() != nil {
			return candidategeneration.ErrInvalid
		}
		// The full pinned intent supplies the ordinal, server time and complete plan.
		// This read has no side effect and precedes report admission.
		var i candidategeneration.Intent
		if err := candidateRequest(ctx, *base, "/v1/candidate-generation-intents/"+id, nil, &i, candidategeneration.MaxIntentBytes); err != nil {
			return err
		}
		if i.Validate() != nil || i.SHA256 != intentSHA || i.Command.ReceiverID != *receiver || i.Command.RequestID != id || i.Command.Plan.SHA256 != planSHA {
			return candidategeneration.ErrInvalid
		}
		if _, err := candidategeneration.NewOutcome(i, r, rec.CapturedAt); err != nil {
			return err
		}
		var o candidategeneration.Outcome
		if err := candidateRequest(ctx, *base, "/v1/candidate-generation-outcomes", r, &o, candidategeneration.MaxOutcomeBytes); err != nil {
			return err
		}
		expected, err := candidategeneration.NewOutcome(i, r, o.RecordedAt)
		if err != nil || !matches(o) || !reflect.DeepEqual(expected, o) {
			return candidategeneration.ErrInvalid
		}
		return emit(out, o)
	}
	if !candidateRegistrySHA.MatchString(outcomeSHA) || artifact != "outcome" && artifact != "recording" && artifact != "output" && artifact != "candidate" && artifact != "source" {
		return candidategeneration.ErrInvalid
	}
	var a candidategeneration.OutcomeArtifact
	if err := candidateRequest(ctx, *base, "/v1/candidate-generation-outcomes/"+id+"/recording", nil, &a, candidategeneration.MaxOutcomeArtifactBytes); err != nil {
		return err
	}
	if a.Validate() != nil || !matches(a.Outcome) || a.Outcome.SHA256 != outcomeSHA {
		return candidategeneration.ErrInvalid
	}
	switch artifact {
	case "recording":
		return emit(out, a.Recording)
	case "output":
		_, err = out.Write(a.Recording.Output)
		return err
	case "candidate", "source":
		if a.Recording.Candidate == nil {
			return candidategeneration.ErrInvalid
		}
		if artifact == "candidate" {
			return emit(out, *a.Recording.Candidate)
		}
		_, err = io.WriteString(out, a.Recording.Candidate.Source.Text)
		return err
	default:
		return emit(out, a.Outcome)
	}
}
