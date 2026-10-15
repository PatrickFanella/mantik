package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/url"
	"strconv"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/domain"
	"git.subcult.tv/subculture-collective/mantik/internal/storage"
)

func runActivity(ctx context.Context, args []string, out, errOut io.Writer) error {
	fs := flag.NewFlagSet("activity", flag.ContinueOnError)
	fs.SetOutput(errOut)
	base := fs.String("api-url", defaultURL, "service HTTP origin")
	format := fs.String("format", "table", "table or json (full structured event details)")
	after := fs.Int64("after", 0, "exclusive event sequence cursor")
	through := fs.String("through", "", "fixed inclusive history boundary; omit for current boundary")
	limit := fs.Int("limit", 25, "events per page, 1..100")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	u, err := url.Parse(*base)
	if err != nil || u.Scheme != "http" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Path != "" && u.Path != "/" {
		return errors.New("api-url must be an HTTP origin without credentials or a path")
	}
	if fs.NArg() != 0 || (*format != "table" && *format != "json") {
		return errors.New("invalid activity arguments")
	}
	q := storage.ActivityQuery{After: *after, Limit: *limit}
	v := url.Values{"after": {strconv.FormatInt(*after, 10)}, "limit": {strconv.Itoa(*limit)}}
	if *through != "" {
		n, err := strconv.ParseInt(*through, 10, 64)
		if err != nil {
			return storage.ErrActivityQuery
		}
		q.Through = &n
		v.Set("through", *through)
	}
	if err := q.Validate(); err != nil {
		return err
	}
	var page domain.ActivityPage
	if err := requestWithQuery(ctx, *base, "/v1/activity", nil, &page, v, 8<<20); err != nil {
		return err
	}
	if *format == "json" {
		return emit(out, page)
	}
	if _, err := fmt.Fprintf(out, "MODE %s (unqualified fixture execution)\nHISTORY THROUGH %d  NEXT AFTER %d  MORE %t\nSEQUENCE  TIME  KIND  EVENT ID\n", strconv.Quote(page.Mode), page.Through, page.NextAfter, page.HasMore); err != nil {
		return err
	}
	for _, item := range page.Items {
		if _, err := fmt.Fprintf(out, "%d  %s  %s  %s\n", item.Sequence, item.Event.At.UTC().Format(time.RFC3339Nano), strconv.Quote(item.Event.Kind), strconv.Quote(item.Event.ID)); err != nil {
			return err
		}
	}
	_, err = fmt.Fprintln(out, "Use --format json for structured evidence; preserve --through when continuing this history. Omit --through to read newly appended events.")
	return err
}
