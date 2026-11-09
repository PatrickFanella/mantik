package cli

import (
	"context"
	"errors"
	"flag"
	"io"
	"net/url"
	"strconv"

	"git.subcult.tv/subculture-collective/mantik/internal/storage"
)

func runAllocationHistory(ctx context.Context, args []string, out, errOut io.Writer) error {
	fs := flag.NewFlagSet("allocation-history", flag.ContinueOnError)
	fs.SetOutput(errOut)
	base := fs.String("api-url", defaultURL, "service HTTP origin")
	receiver := fs.String("receiver-id", "", "expected service receiver")
	decision := fs.String("decision-id", "", "optional decision ID for comparisons and financial effects")
	after := fs.String("after", "", "next_after from a previous page; keeps its original sealed boundary")
	limit := fs.Int("limit", 16, "records per page, 1-64")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	u, err := url.Parse(*base)
	q := storage.OwnedAllocationHistoryQuery{ReceiverID: *receiver, After: *after, Limit: *limit, DecisionID: *decision}
	if err != nil || u.Scheme != "http" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || u.Path != "" && u.Path != "/" || fs.NArg() != 0 || q.Validate() != nil {
		return storage.ErrOwnedAllocationInvalid
	}
	query := url.Values{"receiver_id": {*receiver}, "limit": {strconv.Itoa(*limit)}}
	if *decision != "" {
		query.Set("decision_id", *decision)
	}
	if *after != "" {
		query.Set("after", *after)
	}
	var page storage.OwnedAllocationHistoryPage
	if err := requestBounded(ctx, *base, "/v1/allocation-history", nil, &page, query, storage.MaxOwnedAllocationHistoryBytes, true); err != nil {
		return err
	}
	if err := page.Validate(q); err != nil {
		return err
	}
	return emit(out, page)
}
