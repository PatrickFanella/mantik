package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strconv"
	"strings"

	"git.subcult.tv/subculture-collective/mantik/internal/domain"
)

type caseCatalogFetch func(context.Context) (domain.CaseCatalog, error)
type caseDetailFetch func(context.Context, string) (domain.CaseInspection, error)

const caseSupervisionHelp = "Commands (then Enter): n next | p previous | r refresh catalogue | d CASE_ID current details on this page | q quit"

var supervisionCaseID = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,128}$`)

func validateSupervisionCaseCatalog(c domain.CaseCatalog) error {
	if c.Mode != "paper_fixture" || !supervisionCaseID.MatchString(c.Fixture.ID) || len(c.Routing) > 260 {
		return errors.New("invalid case catalogue mode, fixture identity or capacity")
	}
	previous := ""
	for _, r := range c.Routing {
		if !supervisionCaseID.MatchString(r.ID) || r.ID == c.Fixture.ID || r.ID <= previous {
			return errors.New("case catalogue identities must be valid, unique and sorted")
		}
		previous = r.ID
	}
	return nil
}

func validateSupervisionCaseDetail(d domain.CaseInspection, id string, fixture bool) error {
	if d.Mode != "paper_fixture" {
		return errors.New("invalid case detail mode")
	}
	if fixture {
		if d.Kind != "fixture_execution" || d.Fixture == nil || d.Fixture.ID != id || d.Strategy == nil || d.Strategy.ID != d.Fixture.StrategyVersionID || d.Strategy.ID == "" || d.Routing != nil {
			return errors.New("case detail does not match the selected fixture case and strategy")
		}
	} else if d.Kind != "routing" || d.Routing == nil || d.Routing.ID != id || d.Fixture != nil || d.Strategy != nil {
		return errors.New("case detail does not match the selected routing case")
	}
	return nil
}

func superviseCases(ctx context.Context, in io.ReadCloser, out io.Writer, limit int, fetch caseCatalogFetch, inspect caseDetailFetch) error {
	scanner, cleanup := supervisionInput(ctx, in)
	defer cleanup()
	var catalog domain.CaseCatalog
	offset := 0
	read, stale, haveCatalog := true, true, false
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		if read {
			fresh, err := fetch(ctx)
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if err == nil {
				err = validateSupervisionCaseCatalog(fresh)
			}
			if err != nil {
				stale = true
				if _, err := fmt.Fprintln(out, "STALE: case catalogue request failed. Use r to refresh or q to quit."); err != nil {
					return err
				}
			} else {
				catalog, haveCatalog, stale, offset = fresh, true, false, 0
				if _, err := fmt.Fprintln(out, "READBACK OK: current case catalogue (on demand; not a continuous health check)"); err != nil {
					return err
				}
			}
			read = false
		}
		if haveCatalog {
			if err := renderCasePage(out, catalog, offset, limit, stale); err != nil {
				return err
			}
		}
		if _, err := fmt.Fprintln(out, caseSupervisionHelp); err != nil {
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
		if stale {
			if _, err := fmt.Fprintln(out, "STALE: navigation and details are unavailable until the catalogue refresh succeeds."); err != nil {
				return err
			}
			continue
		}
		switch {
		case len(fields) == 1 && fields[0] == "n":
			if offset+limit < len(catalog.Routing)+1 {
				offset += limit
			} else {
				if _, err := fmt.Fprintln(out, "End of retained catalogue; r refreshes current state."); err != nil {
					return err
				}
			}
		case len(fields) == 1 && fields[0] == "p":
			offset = max(0, offset-limit)
		case len(fields) == 2 && fields[0] == "d":
			if !caseOnPage(catalog, offset, limit, fields[1]) {
				if _, err := fmt.Fprintln(out, "Choose a case identity from the displayed page; no request sent."); err != nil {
					return err
				}
				continue
			}
			detail, err := inspect(ctx, fields[1])
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if err == nil {
				err = validateSupervisionCaseDetail(detail, fields[1], fields[1] == catalog.Fixture.ID)
			}
			if err != nil {
				stale = true
				if _, err := fmt.Fprintln(out, "STALE: case detail request failed; refresh the catalogue before continuing."); err != nil {
					return err
				}
				continue
			}
			if _, err := fmt.Fprintln(out, "CURRENT DETAIL (separate readback; case may have changed since catalogue)"); err != nil {
				return err
			}
			if err := emit(out, detail); err != nil {
				return err
			}
		default:
			if _, err := fmt.Fprintln(out, "Unknown case command; no request sent."); err != nil {
				return err
			}
		}
	}
}

func caseOnPage(c domain.CaseCatalog, offset, limit int, id string) bool {
	for n := offset; n < min(offset+limit, len(c.Routing)+1); n++ {
		if n == 0 && c.Fixture.ID == id || n > 0 && c.Routing[n-1].ID == id {
			return true
		}
	}
	return false
}

func renderCasePage(out io.Writer, c domain.CaseCatalog, offset, limit int, stale bool) error {
	label := "RETAINED CATALOGUE"
	if stale {
		label = "STALE CATALOGUE"
	}
	if _, err := fmt.Fprintf(out, "%s  MODE %s  ITEMS %d..%d OF %d\nKIND  ID  STATUS  BEHAVIOR  MATCHES\n", label, strconv.Quote(c.Mode), offset+1, min(offset+limit, len(c.Routing)+1), len(c.Routing)+1); err != nil {
		return err
	}
	for n := offset; n < min(offset+limit, len(c.Routing)+1); n++ {
		if n == 0 {
			if _, err := fmt.Fprintf(out, "fixture_execution  %s  %s  fixture  -\n", strconv.Quote(c.Fixture.ID), strconv.Quote(c.Fixture.Status)); err != nil {
				return err
			}
		} else {
			v := c.Routing[n-1]
			if _, err := fmt.Fprintf(out, "routing  %s  %s  %s  %d\n", strconv.Quote(v.ID), strconv.Quote(v.Status), strconv.Quote(v.Behavior), v.MatchCount); err != nil {
				return err
			}
		}
	}
	return nil
}
