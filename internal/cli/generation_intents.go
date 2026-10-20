package cli

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"git.subcult.tv/subculture-collective/mantik/internal/candidategeneration"
	"git.subcult.tv/subculture-collective/mantik/internal/review"
	"git.subcult.tv/subculture-collective/mantik/internal/storage"
	"git.subcult.tv/subculture-collective/mantik/internal/workerplan"
	"io"
	"net/url"
)

func runGenerationIntents(ctx context.Context, command string, args []string, out, errOut io.Writer) error {
	fs := flag.NewFlagSet(command, flag.ContinueOnError)
	fs.SetOutput(errOut)
	base := fs.String("api-url", defaultURL, "service HTTP origin")
	receiver := fs.String("receiver-id", "", "required expected research receiver identity")
	var id, publication, publicationSHA, planFile, planSHA, dataFile, intentSHA, artifact string
	if command != "candidate-generation-intents" {
		fs.StringVar(&id, "id", "", "intent request ID")
	}
	if command == "candidate-generation-intent-start" {
		fs.StringVar(&publication, "publication", "", "own retained publication ID")
		fs.StringVar(&publicationSHA, "publication-sha256", "", "required expected publication identity")
		fs.StringVar(&planFile, "plan", "", "immutable offline generation plan file")
		fs.StringVar(&dataFile, "dataset", "", "exact pinned synthetic dataset file")
	}
	if command != "candidate-generation-intents" {
		fs.StringVar(&planSHA, "plan-sha256", "", "required expected plan identity")
	}
	if command == "candidate-generation-intent" {
		fs.StringVar(&intentSHA, "intent-sha256", "", "required expected intent identity")
		fs.StringVar(&artifact, "artifact", "intent", "intent, plan, prompt or exact dataset; no generation")
	}
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	u, err := url.Parse(*base)
	if err != nil || u.Scheme != "http" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Path != "" && u.Path != "/" || fs.NArg() != 0 || !storage.ValidReceiverID(*receiver) {
		return errors.New("generation intent commands require an HTTP origin, explicit receiver-id and no positional arguments")
	}
	if command == "candidate-generation-intents" {
		var h candidategeneration.IntentHistory
		if err := candidateRequest(ctx, *base, "/v1/candidate-generation-intents", nil, &h, candidategeneration.MaxIntentHistoryBytes); err != nil {
			return err
		}
		if h.Validate() != nil || h.ReceiverID != *receiver {
			return candidategeneration.ErrInvalid
		}
		return emit(out, h)
	}
	if !review.ID.MatchString(id) || !candidateRegistrySHA.MatchString(planSHA) {
		return candidategeneration.ErrInvalid
	}
	if command == "candidate-generation-intent" {
		if !candidateRegistrySHA.MatchString(intentSHA) || artifact != "intent" && artifact != "plan" && artifact != "prompt" && artifact != "dataset" {
			return candidategeneration.ErrInvalid
		}
		var a candidategeneration.IntentArtifact
		if err := candidateRequest(ctx, *base, "/v1/candidate-generation-intents/"+id+"/dataset", nil, &a, candidategeneration.MaxIntentArtifactBytes); err != nil {
			return err
		}
		i := a.Intent
		if a.Validate() != nil || i.Command.ReceiverID != *receiver || i.Command.RequestID != id || i.SHA256 != intentSHA || i.Command.Plan.SHA256 != planSHA {
			return candidategeneration.ErrInvalid
		}
		switch artifact {
		case "plan":
			return emit(out, i.Command.Plan)
		case "prompt":
			_, err = io.WriteString(out, i.Command.Plan.Prompt)
			return err
		case "dataset":
			_, err = out.Write(a.Dataset)
			return err
		default:
			return emit(out, i)
		}
	}
	if !review.ID.MatchString(publication) || !candidateRegistrySHA.MatchString(publicationSHA) || planFile == "" || dataFile == "" {
		return candidategeneration.ErrInvalid
	}
	raw, err := externalFile(planFile, candidategeneration.MaxPlanBytes)
	if err != nil {
		return err
	}
	p, err := candidategeneration.ReadPlan(bytes.NewReader(raw))
	if err != nil || p.SHA256 != planSHA {
		return candidategeneration.ErrInvalid
	}
	data, err := externalFile(dataFile, workerplan.MaxInputBytes)
	if err != nil {
		return err
	}
	s := candidategeneration.Submission{Command: candidategeneration.Command{ReceiverID: *receiver, RequestID: id, PublicationID: publication, PublicationSHA256: publicationSHA, Plan: p}, Dataset: data}
	if s.Validate() != nil {
		return candidategeneration.ErrInvalid
	}
	var i candidategeneration.Intent
	if err := candidateRequest(ctx, *base, "/v1/candidate-generation-intents", s, &i, candidategeneration.MaxIntentBytes); err != nil {
		return err
	}
	if i.Validate() != nil || workerplan.Hash(i.Command) != workerplan.Hash(s.Command) {
		return candidategeneration.ErrInvalid
	}
	return emit(out, i)
}
