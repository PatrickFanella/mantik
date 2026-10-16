package cli

import (
	"context"
	"errors"
	"flag"
	"io"
	"net/url"

	"git.subcult.tv/subculture-collective/mantik/internal/review"
)

func runReviewHistory(ctx context.Context, args []string, out, errOut io.Writer) error {
	fs := flag.NewFlagSet("news-reviews", flag.ContinueOnError)
	fs.SetOutput(errOut)
	base := fs.String("api-url", defaultURL, "service HTTP origin")
	id := fs.String("case", "", "exact news case ID; include every retained review attempt")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	u, err := url.Parse(*base)
	if err != nil || u.Scheme != "http" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Path != "" && u.Path != "/" || fs.NArg() != 0 || !review.HistoryCaseID.MatchString(*id) {
		return review.ErrInvalid
	}
	var h review.History
	if err = requestWithQuery(ctx, *base, "/v1/news-cases/"+*id+"/reviews", nil, &h, nil, 1<<20); err != nil {
		return err
	}
	if h.Case.ID != *id || h.Validate() != nil {
		return review.ErrInvalid
	}
	return emit(out, h)
}
