package cli

import (
	"context"
	"errors"
	"flag"
	"io"
	"net/url"

	"git.subcult.tv/subculture-collective/mantik/internal/hypothesis"
	"git.subcult.tv/subculture-collective/mantik/internal/researchhistory"
	"git.subcult.tv/subculture-collective/mantik/internal/review"
	"git.subcult.tv/subculture-collective/mantik/internal/trial"
)

func runResearchHistory(ctx context.Context, command string, args []string, out, errOut io.Writer) error {
	fs := flag.NewFlagSet(command, flag.ContinueOnError)
	fs.SetOutput(errOut)
	base := fs.String("api-url", defaultURL, "service HTTP origin")
	id := fs.String("id", "", "registered trial ID or exact hypothesis ID")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	u, err := url.Parse(*base)
	if err != nil || u.Scheme != "http" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Path != "" && u.Path != "/" || fs.NArg() != 0 {
		return trial.ErrInvalid
	}
	if command == "hypothesis-research-history" {
		if !hypothesis.QuestionID.MatchString(*id) {
			return trial.ErrInvalid
		}
		var q researchhistory.QuestionHistory
		if err = request(ctx, *base, "/v1/hypotheses/"+*id+"/research-history", nil, &q); err != nil {
			return err
		}
		if q.Validate() != nil || q.Native.HypothesisID != *id {
			return trial.ErrInvalid
		}
		return emit(out, q)
	}
	if !review.ID.MatchString(*id) {
		return trial.ErrInvalid
	}
	var h researchhistory.TrialHistory
	if err = request(ctx, *base, "/v1/hypothesis-trials/"+*id+"/research-history", nil, &h); err != nil {
		return err
	}
	if h.Validate() != nil || h.Native.Index.TrialID != *id {
		return trial.ErrInvalid
	}
	return emit(out, h)
}
