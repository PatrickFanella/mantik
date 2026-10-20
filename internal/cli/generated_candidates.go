package cli

import (
	"context"
	"errors"
	"flag"
	"git.subcult.tv/subculture-collective/mantik/internal/generatedcandidate"
	"git.subcult.tv/subculture-collective/mantik/internal/review"
	"git.subcult.tv/subculture-collective/mantik/internal/storage"
	"io"
	"net/url"
)

func runGeneratedCandidates(ctx context.Context, command string, args []string, out, errOut io.Writer) error {
	fs := flag.NewFlagSet(command, flag.ContinueOnError)
	fs.SetOutput(errOut)
	base := fs.String("api-url", defaultURL, "service HTTP origin")
	receiver := fs.String("receiver-id", "", "required expected receiver identity")
	c := generatedcandidate.Command{}
	var linkSHA, registrationSHA, artifact string
	if command != "generated-candidates" {
		fs.StringVar(&c.RequestID, "id", "", "registration request ID")
	}
	if command == "generated-candidate-register" {
		fs.StringVar(&c.ParentID, "parent", "", "optional earlier candidate registration ID")
		fs.StringVar(&c.GenerationRequestID, "generation-id", "", "owner-retained generation request ID")
		fs.StringVar(&c.IntentSHA256, "intent-sha256", "", "required expected generation intent identity")
		fs.StringVar(&c.OutcomeSHA256, "outcome-sha256", "", "required expected generation outcome identity")
		fs.StringVar(&c.RecordingSHA256, "recording-sha256", "", "required expected recording identity")
		fs.StringVar(&c.PlanSHA256, "plan-sha256", "", "required expected generation plan identity")
		fs.StringVar(&c.CandidateSHA256, "candidate-sha256", "", "required expected recorded candidate identity")
	} else if command == "generated-candidate" {
		fs.StringVar(&linkSHA, "link-sha256", "", "required expected provenance link identity")
		fs.StringVar(&registrationSHA, "registration-sha256", "", "required expected legacy registration identity")
		fs.StringVar(&artifact, "artifact", "link", "link|artifact|registration|generation|recording|candidate|source|output")
	}
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	u, err := url.Parse(*base)
	if err != nil || u.Scheme != "http" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Path != "" && u.Path != "/" || fs.NArg() != 0 || !storage.ValidReceiverID(*receiver) {
		return generatedcandidate.ErrInvalid
	}
	c.ReceiverID = *receiver
	if command == "generated-candidates" {
		var h generatedcandidate.Registry
		if err := candidateRequest(ctx, *base, "/v1/generated-candidates", nil, &h, generatedcandidate.MaxRegistryBytes); err != nil {
			return err
		}
		if h.Validate() != nil || h.ReceiverID != *receiver {
			return generatedcandidate.ErrInvalid
		}
		return emit(out, h)
	}
	var a generatedcandidate.Artifact
	if command == "generated-candidate-register" {
		if c.Validate() != nil {
			return generatedcandidate.ErrInvalid
		}
		if err := candidateRequest(ctx, *base, "/v1/generated-candidates", c, &a, generatedcandidate.MaxArtifactBytes); err != nil {
			return err
		}
		if a.Validate() != nil || a.Link.Command != c {
			return generatedcandidate.ErrInvalid
		}
		return emit(out, a)
	}
	if !review.ID.MatchString(c.RequestID) || !candidateRegistrySHA.MatchString(linkSHA) || !candidateRegistrySHA.MatchString(registrationSHA) {
		return generatedcandidate.ErrInvalid
	}
	switch artifact {
	case "link", "artifact", "registration", "generation", "recording", "candidate", "source", "output":
	default:
		return generatedcandidate.ErrInvalid
	}
	// Every selected export first validates the same complete artifact and both
	// expected identities. A compact link alone cannot rederive source provenance.
	if err := candidateRequest(ctx, *base, "/v1/generated-candidates/"+c.RequestID, nil, &a, generatedcandidate.MaxArtifactBytes); err != nil {
		return err
	}
	if a.Validate() != nil || a.Link.Command.ReceiverID != *receiver || a.Link.Command.RequestID != c.RequestID || a.Link.SHA256 != linkSHA || a.Registration.SHA256 != registrationSHA {
		return generatedcandidate.ErrInvalid
	}
	switch artifact {
	case "artifact":
		return emit(out, a)
	case "registration":
		return emit(out, a.Registration)
	case "generation":
		return emit(out, a.Generation)
	case "recording":
		return emit(out, a.Generation.Recording)
	case "candidate":
		return emit(out, a.Registration.Candidate)
	case "source":
		_, err = io.WriteString(out, a.Registration.Candidate.Source.Text)
		return err
	case "output":
		_, err = out.Write(a.Generation.Recording.Output)
		return err
	default:
		return emit(out, a.Link)
	}
}
