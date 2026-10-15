package cli

import (
	"context"
	"fmt"
	"io"
	"strings"

	"git.subcult.tv/subculture-collective/mantik/internal/experiment"
)

type libraryFetch func(context.Context) (experiment.Catalog, error)

const librarySupervisionHelp = "Commands (then Enter): n next | p previous | r refresh catalogue | d METHOD_ID displayed definition/hash | q quit"

func superviseLibrary(ctx context.Context, in io.ReadCloser, out io.Writer, limit int, fetch libraryFetch) error {
	scanner, cleanup := supervisionInput(ctx, in)
	defer cleanup()
	var catalog experiment.Catalog
	offset := 0
	read, stale, haveCatalog := true, true, false
	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if read {
			fresh, err := fetch(ctx)
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if err == nil {
				err = fresh.Validate()
			}
			if err != nil {
				stale = true
				if _, err := fmt.Fprintln(out, "STALE: compiled library readback failed or was invalid. Use r to refresh or q to quit."); err != nil {
					return err
				}
			} else {
				catalog, haveCatalog, stale, offset = fresh, true, false, 0
				if _, err := fmt.Fprintln(out, "READBACK OK: compiled method catalogue (not active admission or evaluation results)"); err != nil {
					return err
				}
			}
			read = false
		}
		if haveCatalog {
			label := "RETAINED LIBRARY PAGE"
			if stale {
				label = "STALE LIBRARY PAGE"
			}
			end := min(offset+limit, len(catalog.Methods))
			if _, err := fmt.Fprintf(out, "%s  ITEMS %d..%d OF %d\n", label, offset+1, end, len(catalog.Methods)); err != nil {
				return err
			}
			page := catalog
			page.Methods = catalog.Methods[offset:end]
			if err := renderMethodCatalog(out, page); err != nil {
				return err
			}
		}
		if _, err := fmt.Fprintln(out, librarySupervisionHelp); err != nil {
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
			if _, err := fmt.Fprintln(out, "STALE: navigation and details are unavailable until a successful catalogue read."); err != nil {
				return err
			}
			continue
		}
		switch {
		case len(fields) == 1 && fields[0] == "n":
			if offset+limit < len(catalog.Methods) {
				offset += limit
			} else {
				if _, err := fmt.Fprintln(out, "End of compiled catalogue; r refreshes from the service."); err != nil {
					return err
				}
			}
		case len(fields) == 1 && fields[0] == "p":
			offset = max(0, offset-limit)
		case len(fields) == 2 && fields[0] == "d":
			found := false
			for _, m := range catalog.Methods[offset:min(offset+limit, len(catalog.Methods))] {
				if m.ID == fields[1] {
					if _, err := fmt.Fprintln(out, "DISPLAYED COMPILED METHOD PIN (retained catalogue; no experiment or activation request)"); err != nil {
						return err
					}
					if err := emit(out, m); err != nil {
						return err
					}
					found = true
					break
				}
			}
			if !found {
				if _, err := fmt.Fprintln(out, "Choose a method identity from the displayed page; no request sent."); err != nil {
					return err
				}
			}
		default:
			if _, err := fmt.Fprintln(out, "Unknown library command; no request sent."); err != nil {
				return err
			}
		}
	}
}
