package cli

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/url"
	"os"
	"strconv"
	"strings"

	"git.subcult.tv/subculture-collective/mantik/internal/domain"
	"git.subcult.tv/subculture-collective/mantik/internal/experiment"
	"git.subcult.tv/subculture-collective/mantik/internal/review"
	"git.subcult.tv/subculture-collective/mantik/internal/storage"
)

type activityFetch func(context.Context, storage.ActivityQuery) (domain.ActivityPage, error)

func runSupervise(ctx context.Context, args []string, out, errOut io.Writer) error {
	fs := flag.NewFlagSet("supervise", flag.ContinueOnError)
	fs.SetOutput(errOut)
	base := fs.String("api-url", defaultURL, "service HTTP origin")
	limit := fs.Int("limit", 10, "items per page, 1..100")
	view := fs.String("view", "activity", "activity, cases, news, portfolio, decisions, exceptions or library")
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
	if fs.NArg() != 0 || (*view != "activity" && *view != "cases" && *view != "news" && *view != "exceptions" && *view != "library" && *view != "portfolio" && *view != "decisions") {
		return errors.New("unexpected supervision arguments")
	}
	if err := (storage.ActivityQuery{Limit: *limit}).Validate(); err != nil {
		return err
	}
	// Own a separate Unix stdin handle so cancellation can unblock a terminal
	// read without closing the caller's os.Stdin or leaving a reader goroutine.
	in, err := os.Open("/dev/stdin")
	if err != nil {
		return errors.New("supervise requires a readable Unix stdin")
	}
	if *view == "portfolio" {
		return supervisePortfolio(ctx, in, out, *limit, func(ctx context.Context) (portfolioReadback, error) { return fetchPortfolio(ctx, *base) })
	}
	if *view == "decisions" {
		return superviseDecisions(ctx, in, out, *limit, func(ctx context.Context) (storage.PaperDecisionCatalog, error) {
			var c storage.PaperDecisionCatalog
			err := requestBounded(ctx, *base, "/v1/paper-decisions", nil, &c, nil, 1<<20, true)
			return c, err
		}, func(ctx context.Context, d storage.PaperDecisionRecord, kind string) (any, error) {
			return fetchDecisionInspection(ctx, *base, d, kind)
		})
	}
	if *view == "news" {
		fetch := func(ctx context.Context) (storage.NewsCatalog, error) { return fetchNewsCatalog(ctx, *base) }
		inspect := func(ctx context.Context, key string, original bool) (jsonNewsInspection, error) {
			return fetchNewsRecord(ctx, *base, key, original)
		}
		history := func(ctx context.Context, id string) (review.History, error) { return fetchNewsHistory(ctx, *base, id) }
		attempt := func(ctx context.Context, id string) (review.View, error) { return fetchNewsAttempt(ctx, *base, id) }
		return superviseNews(ctx, in, out, *limit, fetch, inspect, history, attempt)
	}
	if *view == "library" {
		fetch := func(ctx context.Context) (experiment.Catalog, error) {
			var c experiment.Catalog
			err := request(ctx, *base, "/v1/library", nil, &c)
			return c, err
		}
		return superviseLibrary(ctx, in, out, *limit, fetch)
	}
	if *view == "exceptions" {
		fetch := func(ctx context.Context, q storage.ExceptionQuery) (domain.ExceptionPage, error) {
			v := url.Values{"limit": {strconv.Itoa(q.Limit)}}
			if q.After != "" {
				v.Set("after", q.After)
			}
			var page domain.ExceptionPage
			err := requestWithQuery(ctx, *base, "/v1/exceptions", nil, &page, v, 8<<20)
			return page, err
		}
		return superviseExceptions(ctx, in, out, *limit, fetch)
	}
	if *view == "cases" {
		catalog := func(ctx context.Context) (domain.CaseCatalog, error) {
			var c domain.CaseCatalog
			err := request(ctx, *base, "/v1/cases", nil, &c)
			return c, err
		}
		detail := func(ctx context.Context, id string) (domain.CaseInspection, error) {
			var d domain.CaseInspection
			err := request(ctx, *base, "/v1/cases/"+id, nil, &d)
			return d, err
		}
		return superviseCases(ctx, in, out, *limit, catalog, detail)
	}
	fetch := func(ctx context.Context, q storage.ActivityQuery) (domain.ActivityPage, error) {
		v := url.Values{"after": {strconv.FormatInt(q.After, 10)}, "limit": {strconv.Itoa(q.Limit)}}
		if q.Through != nil {
			v.Set("through", strconv.FormatInt(*q.Through, 10))
		}
		var page domain.ActivityPage
		err := requestWithQuery(ctx, *base, "/v1/activity", nil, &page, v, 8<<20)
		return page, err
	}
	return superviseActivity(ctx, in, out, *limit, fetch)
}

const supervisionHelp = "Commands (then Enter): n next | r retry current request | b begin fresh history | u appended history (at end) | d SEQUENCE details on this page | q quit"

// Validate the response before using its cursor or exposing event details.
// Audit sequences may have gaps, but must advance within the requested window.
func validateActivityPage(page domain.ActivityPage, q storage.ActivityQuery) error {
	if err := q.Validate(); err != nil {
		return err
	}
	if page.Mode != "paper_fixture" || page.Through < q.After || len(page.Items) > q.Limit || q.Through != nil && page.Through != *q.Through {
		return errors.New("activity readback does not match the requested window")
	}
	previous := q.After
	for _, item := range page.Items {
		if item.Sequence <= previous || item.Sequence > page.Through {
			return errors.New("activity readback sequences are outside the requested order or window")
		}
		previous = item.Sequence
	}
	if page.NextAfter != previous || page.HasMore && (len(page.Items) != q.Limit || previous >= page.Through) {
		return errors.New("activity readback cursor cannot advance safely")
	}
	return nil
}

// superviseActivity owns in. Only one page and its pending request are retained.
func superviseActivity(ctx context.Context, in io.ReadCloser, out io.Writer, limit int, fetch activityFetch) error {
	scanner, cleanup := supervisionInput(ctx, in)
	defer cleanup()
	query := storage.ActivityQuery{Limit: limit}
	var page domain.ActivityPage
	stale, havePage, read := true, false, true
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		if read {
			fresh, err := fetch(ctx, query)
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if err == nil {
				err = validateActivityPage(fresh, query)
			}
			if err != nil {
				stale = true
				if _, err := fmt.Fprintln(out, "STALE: activity request failed; displayed history is not a fresh read. Use r to retry or b to begin again."); err != nil {
					return err
				}
			} else {
				page, havePage, stale = fresh, true, false
				boundary := fresh.Through
				query.Through = &boundary
				if _, err := fmt.Fprintln(out, "READBACK OK (on demand; not a continuous health check)"); err != nil {
					return err
				}
			}
			if havePage {
				if err := renderActivity(out, page); err != nil {
					return err
				}
			}
			read = false
		}
		if _, err := fmt.Fprintln(out, supervisionHelp); err != nil {
			return err
		}
		if _, err := fmt.Fprint(out, "> "); err != nil {
			return err
		}
		if !scanner.Scan() {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return scanner.Err()
		}
		fields := strings.Fields(scanner.Text())
		if len(fields) == 1 && fields[0] == "q" {
			return nil
		}
		if len(fields) == 1 && fields[0] == "r" {
			read = true
			continue
		}
		if len(fields) == 1 && fields[0] == "b" {
			query = storage.ActivityQuery{Limit: limit}
			read = true
			continue
		}
		if stale {
			if _, err := fmt.Fprintln(out, "STALE: navigation and details are unavailable until a successful readback."); err != nil {
				return err
			}
			continue
		}
		switch {
		case len(fields) == 1 && fields[0] == "n":
			if !page.HasMore {
				if _, err := fmt.Fprintln(out, "End of fixed history. Use u for appended events or b for a new readback."); err != nil {
					return err
				}
				continue
			}
			query.After = page.NextAfter
			read = true
		case len(fields) == 1 && fields[0] == "u":
			if page.HasMore {
				if _, err := fmt.Fprintln(out, "Finish this fixed history with n before reading appended events."); err != nil {
					return err
				}
				continue
			}
			query.After, query.Through, read = page.NextAfter, nil, true
		case len(fields) == 2 && fields[0] == "d":
			sequence, err := strconv.ParseInt(fields[1], 10, 64)
			found := false
			for _, item := range page.Items {
				if err == nil && item.Sequence == sequence {
					if err := emit(out, item); err != nil {
						return err
					}
					found = true
					break
				}
			}
			if !found {
				if _, err := fmt.Fprintln(out, "Choose an event sequence from the displayed page."); err != nil {
					return err
				}
			}
		default:
			if _, err := fmt.Fprintln(out, "Unknown command; no request sent."); err != nil {
				return err
			}
		}
	}
}

func supervisionInput(ctx context.Context, in io.ReadCloser) (*bufio.Scanner, func()) {
	stop := context.AfterFunc(ctx, func() { _ = in.Close() })
	scanner := bufio.NewScanner(in)
	scanner.Buffer(make([]byte, 128), 256)
	return scanner, func() { stop(); _ = in.Close() }
}
