package cli

import (
	"context"
	"errors"
	"flag"
	"io"
	"net/url"
	"os"

	"git.subcult.tv/subculture-collective/mantik/internal/hypothesis"
	"git.subcult.tv/subculture-collective/mantik/internal/review"
	"git.subcult.tv/subculture-collective/mantik/internal/storage"
)

func runHypothesisPublication(ctx context.Context, command string, args []string, out, errOut io.Writer) error {
	fs := flag.NewFlagSet(command, flag.ContinueOnError)
	fs.SetOutput(errOut)
	base := fs.String("api-url", defaultURL, "service HTTP origin")
	var id string
	if command != "hypotheses" {
		fs.StringVar(&id, "id", "", "publication request ID, or hypothesis ID for inspection")
	}
	var receiver, reviewID, file string
	if command == "hypothesis-publish" {
		fs.StringVar(&receiver, "receiver-id", "", "explicit pinned research receiver identity")
		fs.StringVar(&reviewID, "review", "", "completed current service review ID")
		fs.StringVar(&file, "file", "", "private full declaration JSON file")
	}
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	u, err := url.Parse(*base)
	if err != nil || u.Scheme != "http" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Path != "" && u.Path != "/" || fs.NArg() != 0 {
		return errors.New("hypothesis commands require an HTTP origin without credentials, path or positional arguments")
	}
	if command == "hypotheses" {
		var c hypothesis.Catalogue
		if err = requestWithQuery(ctx, *base, "/v1/hypotheses", nil, &c, nil, 4<<20); err != nil {
			return err
		}
		if err = c.Validate(); err != nil {
			return err
		}
		return emit(out, c)
	}
	if command == "hypothesis" {
		if !hypothesis.QuestionID.MatchString(id) {
			return hypothesis.ErrInvalid
		}
		var q hypothesis.Question
		if err = request(ctx, *base, "/v1/hypotheses/"+id, nil, &q); err != nil {
			return err
		}
		if q.ID != id || q.Schema != hypothesis.QuestionSchema || q.Authority != "research_declaration_only" || q.QualificationCredit {
			return errors.New("hypothesis response conflicts with request")
		}
		return emit(out, q)
	}
	if !storage.ValidReceiverID(receiver) || !review.ID.MatchString(id) || !review.ID.MatchString(reviewID) || file == "" {
		return errors.New("hypothesis publication requires receiver-id, id, review and file")
	}
	f, err := os.Open(file)
	if err != nil {
		return errors.New("cannot open hypothesis declaration")
	}
	d, readErr := hypothesis.Read(f)
	if err = errors.Join(readErr, f.Close()); err != nil {
		return err
	}
	sealed, err := hypothesis.Seal(d)
	if err != nil {
		return err
	}
	body := hypothesis.PublicationCommand{ReceiverID: receiver, RequestID: id, ReviewID: reviewID, Declaration: d}
	var p hypothesis.Publication
	if err = request(ctx, *base, "/v1/hypothesis-publications", body, &p); err != nil {
		return err
	}
	if p.Schema != hypothesis.PublicationSchema || p.ID != id || !p.OriginBindingVerified || !hypothesis.SamePublication(p, receiver, reviewID, sealed) || p.Sealed.ID != sealed.ID || p.Sealed.Authority != "research_declaration_only" || p.Sealed.QualificationCredit {
		return errors.New("hypothesis receipt conflicts with pinned declaration")
	}
	return emit(out, p)
}
