package cli

import (
	"context"
	"errors"
	"flag"
	"io"
	"net/url"
	"strconv"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/storage"
)

// runAllocationContribution reads every native contribution page of one
// decision at a fixed cut and emits their validated sum.
func runAllocationContribution(ctx context.Context, args []string, out, errOut io.Writer) error {
	fs := flag.NewFlagSet("allocation-contribution", flag.ContinueOnError)
	fs.SetOutput(errOut)
	base := fs.String("api-url", defaultURL, "service HTTP origin")
	receiver := fs.String("receiver-id", "", "expected service receiver")
	decision := fs.String("decision-id", "", "decision whose reserved funding and execution effects are summed")
	limit := fs.Int("limit", 16, "sealed records per request, 1-64")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	u, err := url.Parse(*base)
	q := storage.OwnedAllocationContributionQuery{ReceiverID: *receiver, DecisionID: *decision, Limit: *limit}
	if err != nil || u.Scheme != "http" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || u.Path != "" && u.Path != "/" || fs.NArg() != 0 || q.Validate() != nil {
		return storage.ErrOwnedAllocationInvalid
	}
	pages := []storage.OwnedAllocationContributionPage{}
	for {
		if len(pages) == storage.MaxOwnedAllocationContributionPages {
			return storage.ErrOwnedAllocationCapacity
		}
		query := url.Values{"receiver_id": {*receiver}, "decision_id": {*decision}, "limit": {strconv.Itoa(*limit)}}
		if q.After != "" {
			query.Set("after", q.After)
		}
		var page storage.OwnedAllocationContributionPage
		if err := requestBoundedWithTimeout(ctx, *base, "/v1/allocation-history/contributions", nil, &page, query, storage.MaxOwnedAllocationContributionBytes, true, 35*time.Second); err != nil {
			return err
		}
		if err := page.Validate(q); err != nil {
			return err
		}
		pages = append(pages, page)
		if page.NextAfter == "" {
			break
		}
		q.After = page.NextAfter
	}
	total, err := storage.AssembleOwnedAllocationContribution(*receiver, *decision, *limit, pages)
	if err != nil {
		return err
	}
	return emit(out, total)
}
