package cli

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"io"
	"net/url"
	"regexp"

	"git.subcult.tv/subculture-collective/mantik/internal/candidate"
	"git.subcult.tv/subculture-collective/mantik/internal/candidatelineage"
	"git.subcult.tv/subculture-collective/mantik/internal/review"
	"git.subcult.tv/subculture-collective/mantik/internal/storage"
	"git.subcult.tv/subculture-collective/mantik/internal/workerplan"
)

var candidateRegistrySHA = regexp.MustCompile(`^[a-f0-9]{64}$`)

func candidateRequest(ctx context.Context, base, path string, input, output any, limit int64) error {
	return requestBounded(ctx, base, path, input, output, nil, limit, true)
}

func runCandidateRegistry(ctx context.Context, command string, args []string, out, errOut io.Writer) error {
	fs := flag.NewFlagSet(command, flag.ContinueOnError)
	fs.SetOutput(errOut)
	base := fs.String("api-url", defaultURL, "service HTTP origin")
	receiverID := fs.String("receiver-id", "", "required expected research receiver identity")
	var id, publication, parent, file, expected, data, registrationSHA, format string
	if command != "candidate-registrations" {
		fs.StringVar(&id, "id", "", "registration request ID")
	}
	if command == "candidate-register" {
		fs.StringVar(&publication, "publication", "", "retained hypothesis publication ID")
		fs.StringVar(&parent, "parent", "", "optional earlier parent registration")
		fs.StringVar(&file, "candidate", "", "sealed candidate package file")
		fs.StringVar(&expected, "candidate-sha256", "", "required expected candidate identity")
		fs.StringVar(&data, "dataset", "", "exact pinned dataset file")
	}
	if command == "candidate-registration" || command == "candidate-dataset" {
		fs.StringVar(&registrationSHA, "registration-sha256", "", "required expected registration identity")
	}
	if command == "candidate-dataset" {
		fs.StringVar(&format, "format", "json", "json artifact or raw exact dataset bytes")
	}
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	u, err := url.Parse(*base)
	if err != nil || u.Scheme != "http" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Path != "" && u.Path != "/" || fs.NArg() != 0 || !storage.ValidReceiverID(*receiverID) {
		return errors.New("candidate registry commands require an HTTP origin, explicit receiver-id and no positional arguments")
	}
	if command == "candidate-registrations" {
		var r candidatelineage.Registry
		if err = candidateRequest(ctx, *base, "/v1/candidates", nil, &r, candidatelineage.MaxRegistryBytes); err != nil {
			return err
		}
		if r.Validate() != nil || r.ReceiverID != *receiverID {
			return candidatelineage.ErrInvalid
		}
		return emit(out, r)
	}
	if !review.ID.MatchString(id) {
		return candidatelineage.ErrInvalid
	}
	if command == "candidate-registration" || command == "candidate-dataset" {
		if !candidateRegistrySHA.MatchString(registrationSHA) {
			return candidatelineage.ErrInvalid
		}
		if command == "candidate-dataset" {
			if format != "json" && format != "raw" {
				return candidatelineage.ErrInvalid
			}
			var a candidatelineage.Artifact
			if err = candidateRequest(ctx, *base, "/v1/candidates/"+id+"/dataset", nil, &a, candidatelineage.MaxSubmissionBytes); err != nil {
				return err
			}
			if a.Validate() != nil || a.Registration.ID != id || a.Registration.ReceiverID != *receiverID || a.Registration.SHA256 != registrationSHA {
				return candidatelineage.ErrInvalid
			}
			if format == "raw" {
				_, err = out.Write(a.Dataset)
				return err
			}
			return emit(out, a)
		}
		var r candidatelineage.Registration
		if err = candidateRequest(ctx, *base, "/v1/candidates/"+id, nil, &r, candidatelineage.MaxBytes); err != nil {
			return err
		}
		if r.Validate() != nil || r.ID != id || r.ReceiverID != *receiverID || r.SHA256 != registrationSHA {
			return candidatelineage.ErrInvalid
		}
		return emit(out, r)
	}
	if file == "" || data == "" || !candidateRegistrySHA.MatchString(expected) {
		return candidatelineage.ErrInvalid
	}
	raw, err := externalFile(file, candidate.MaxPackageBytes)
	if err != nil {
		return err
	}
	p, err := candidate.Read(bytes.NewReader(raw))
	if err != nil || p.SHA256 != expected {
		return candidatelineage.ErrInvalid
	}
	input, err := externalFile(data, workerplan.MaxInputBytes)
	if err != nil || p.VerifyDataset(input) != nil {
		return candidatelineage.ErrInvalid
	}
	c := candidatelineage.Command{ReceiverID: *receiverID, RequestID: id, PublicationID: publication, ParentID: parent, Candidate: p}
	if c.Validate() != nil {
		return candidatelineage.ErrInvalid
	}
	var registered candidatelineage.Registration
	if err = candidateRequest(ctx, *base, "/v1/candidates", candidatelineage.Submission{Command: c, Dataset: input}, &registered, candidatelineage.MaxBytes); err != nil {
		return err
	}
	if registered.Validate() != nil || registered.ID != c.RequestID || registered.ReceiverID != c.ReceiverID || registered.PublicationID != c.PublicationID || registered.ParentID != c.ParentID || registered.Candidate.SHA256 != c.Candidate.SHA256 {
		return candidatelineage.ErrInvalid
	}
	return emit(out, registered)
}
