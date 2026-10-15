package cli

import (
	"context"
	"errors"
	"flag"
	"io"
	"net/url"
	"os"

	"git.subcult.tv/subculture-collective/mantik/internal/experimentplan"
	"git.subcult.tv/subculture-collective/mantik/internal/hypothesis"
	"git.subcult.tv/subculture-collective/mantik/internal/review"
	"git.subcult.tv/subculture-collective/mantik/internal/storage"
	"git.subcult.tv/subculture-collective/mantik/internal/trial"
)

func runTrial(ctx context.Context, command string, args []string, out, errOut io.Writer) error {
	fs := flag.NewFlagSet(command, flag.ContinueOnError)
	fs.SetOutput(errOut)
	base := fs.String("api-url", defaultURL, "service HTTP origin")
	id := fs.String("id", "", "trial request ID, or hypothesis ID for trial history")
	var receiver, publication, parent, file, digest string
	if command == "hypothesis-trial-register" {
		fs.StringVar(&receiver, "receiver-id", "", "explicit research receiver identity")
		fs.StringVar(&publication, "publication", "", "retained hypothesis publication ID")
		fs.StringVar(&parent, "parent", "", "optional existing parent trial ID")
		fs.StringVar(&file, "recipe", "", "sealed experiment recipe file")
		fs.StringVar(&digest, "recipe-sha256", "", "expected recipe identity")
	}
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	u, err := url.Parse(*base)
	if err != nil || u.Scheme != "http" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Path != "" && u.Path != "/" || fs.NArg() != 0 {
		return errors.New("trial commands require an HTTP origin without credentials, path or positional arguments")
	}
	if command == "hypothesis-trials" {
		if !hypothesis.QuestionID.MatchString(*id) {
			return trial.ErrInvalid
		}
		var h trial.History
		if err = request(ctx, *base, "/v1/hypotheses/"+*id+"/trials", nil, &h); err != nil {
			return err
		}
		if h.Schema != trial.HistorySchema || h.HypothesisID != *id || h.Authority != "research_trial_declaration_only" || h.QualificationCredit {
			return errors.New("trial history conflicts with request")
		}
		return emit(out, h)
	}
	if !review.ID.MatchString(*id) {
		return trial.ErrInvalid
	}
	var registered trial.Registration
	if command == "hypothesis-trial" {
		if err = request(ctx, *base, "/v1/hypothesis-trials/"+*id, nil, &registered); err != nil {
			return err
		}
		if registered.ID != *id || registered.Validate() != nil {
			return errors.New("invalid trial receipt")
		}
		return emit(out, registered)
	}
	if !storage.ValidReceiverID(receiver) || !review.ID.MatchString(publication) || file == "" || digest == "" {
		return errors.New("trial registration requires receiver-id, id, publication, recipe and recipe-sha256")
	}
	f, err := os.Open(file)
	if err != nil {
		return errors.New("cannot open trial recipe")
	}
	recipe, readErr := experimentplan.ReadRecipe(f)
	if err = errors.Join(readErr, f.Close()); err != nil {
		return err
	}
	if recipe.SHA256 != digest {
		return errors.New("trial recipe does not match expected identity")
	}
	c := trial.Command{ReceiverID: receiver, RequestID: *id, PublicationID: publication, ParentID: parent, Recipe: recipe}
	if err = c.Validate(); err != nil {
		return err
	}
	if err = request(ctx, *base, "/v1/hypothesis-trials", c, &registered); err != nil {
		return err
	}
	if registered.ID != *id || registered.Validate() != nil || !trial.SameRequest(registered, c) {
		return errors.New("trial receipt conflicts with pinned request")
	}
	return emit(out, registered)
}
