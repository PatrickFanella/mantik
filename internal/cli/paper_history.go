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

func runPaperHistory(ctx context.Context, args []string, out, errOut io.Writer) error {
	fs := flag.NewFlagSet("paper-history", flag.ContinueOnError)
	fs.SetOutput(errOut)
	base := fs.String("api-url", defaultURL, "service HTTP origin")
	receiver := fs.String("receiver-id", "", "expected service receiver")
	kind := fs.String("kind", "decision", "decision or outcome")
	after := fs.String("after", "", "next_after from a previous page; keeps its original archive boundary")
	limit := fs.Int("limit", 16, "records per page, 1-64")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	u, err := url.Parse(*base)
	q := storage.PaperHistoryQuery{ReceiverID: *receiver, Kind: *kind, After: *after, Limit: *limit}
	if err != nil || u.Scheme != "http" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || u.Path != "" && u.Path != "/" || fs.NArg() != 0 || q.Validate() != nil {
		return storage.ErrPaperRecordArchiveInvalid
	}
	query := url.Values{"receiver_id": {*receiver}, "kind": {*kind}, "limit": {strconv.Itoa(*limit)}}
	if *after != "" {
		query.Set("after", *after)
	}
	var page storage.PaperHistoryPage
	if err := requestBounded(ctx, *base, "/v1/paper-history", nil, &page, query, storage.MaxPaperHistoryBytes, true); err != nil {
		return err
	}
	if err := page.Validate(q); err != nil {
		return err
	}
	return emit(out, page)
}
