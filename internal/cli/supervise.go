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
	"git.subcult.tv/subculture-collective/mantik/internal/storage"
)

type activityFetch func(context.Context, storage.ActivityQuery) (domain.ActivityPage, error)

func runSupervise(ctx context.Context, args []string, out, errOut io.Writer) error {
	fs := flag.NewFlagSet("supervise", flag.ContinueOnError)
	fs.SetOutput(errOut)
	base := fs.String("api-url", defaultURL, "service HTTP origin")
	limit := fs.Int("limit", 10, "events per page, 1..100")
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
	if fs.NArg() != 0 {
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

// superviseActivity owns in. Only one page and its pending request are retained.
func superviseActivity(ctx context.Context, in io.ReadCloser, out io.Writer, limit int, fetch activityFetch) error {
	defer in.Close()
	stop := context.AfterFunc(ctx, func() { _ = in.Close() })
	defer stop()
	scanner := bufio.NewScanner(in)
	scanner.Buffer(make([]byte, 128), 256)
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
