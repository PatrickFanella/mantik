package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"

	"git.subcult.tv/subculture-collective/mantik/internal/domain"
	"git.subcult.tv/subculture-collective/mantik/internal/storage"
)

type exceptionFetch func(context.Context, storage.ExceptionQuery) (domain.ExceptionPage, error)

const exceptionSupervisionHelp = "Commands (then Enter): n next | r retry current request | b begin first page | d EXCEPTION_ID retained details on this page | q quit"

func validateExceptionPage(p domain.ExceptionPage, q storage.ExceptionQuery) error {
	if p.Mode != "paper_fixture" || p.Scope != "retained_exceptions" || len(p.Items) > q.Limit {
		return errors.New("invalid exception page scope or bound")
	}
	previous := q.After
	for _, e := range p.Items {
		if e.ID <= previous || (storage.ExceptionQuery{After: e.ID, Limit: 1}).Validate() != nil {
			return errors.New("invalid exception page order")
		}
		previous = e.ID
	}
	if p.NextAfter != previous || p.HasMore && len(p.Items) == 0 {
		return errors.New("invalid exception page cursor")
	}
	return nil
}

func superviseExceptions(ctx context.Context, in io.ReadCloser, out io.Writer, limit int, fetch exceptionFetch) error {
	scanner, cleanup := supervisionInput(ctx, in)
	defer cleanup()
	query := storage.ExceptionQuery{Limit: limit}
	var page domain.ExceptionPage
	read, stale, havePage := true, true, false
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
				err = validateExceptionPage(fresh, query)
			}
			if err != nil {
				stale = true
				if _, err := fmt.Fprintln(out, "STALE: exception page request failed or returned an invalid page. Use r to retry or b to begin again."); err != nil {
					return err
				}
			} else {
				page, havePage, stale = fresh, true, false
				if _, err := fmt.Fprintln(out, "READBACK OK: retained exception page (on demand; not current readiness)"); err != nil {
					return err
				}
			}
			if havePage {
				if stale {
					if _, err := fmt.Fprintln(out, "STALE PAGE: last successful retained records"); err != nil {
						return err
					}
				}
				if err := renderExceptionPage(out, page); err != nil {
					return err
				}
			}
			read = false
		}
		if _, err := fmt.Fprintln(out, exceptionSupervisionHelp); err != nil {
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
			query = storage.ExceptionQuery{Limit: limit}
			read = true
			continue
		}
		if stale {
			if _, err := fmt.Fprintln(out, "STALE: navigation and details are unavailable until a successful page read."); err != nil {
				return err
			}
			continue
		}
		switch {
		case len(fields) == 1 && fields[0] == "n":
			if !page.HasMore {
				if _, err := fmt.Fprintln(out, "End of retained traversal. Use b to discover updated records or newly inserted identities."); err != nil {
					return err
				}
				continue
			}
			query.After = page.NextAfter
			read = true
		case len(fields) == 2 && fields[0] == "d":
			found := false
			for _, e := range page.Items {
				if e.ID == fields[1] {
					if _, err := fmt.Fprintln(out, "RETAINED DETAIL FROM DISPLAYED PAGE (use r to refresh; no recovery request sent)"); err != nil {
						return err
					}
					if err := emit(out, e); err != nil {
						return err
					}
					found = true
					break
				}
			}
			if !found {
				if _, err := fmt.Fprintln(out, "Choose an exception identity from the displayed page; no request sent."); err != nil {
					return err
				}
			}
		default:
			if _, err := fmt.Fprintln(out, "Unknown exception command; no request sent."); err != nil {
				return err
			}
		}
	}
}
