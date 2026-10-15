package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/url"
	"strconv"

	"git.subcult.tv/subculture-collective/mantik/internal/experiment"
)

func runLibrary(ctx context.Context, args []string, out, errOut io.Writer) error {
	fs := flag.NewFlagSet("library", flag.ContinueOnError)
	fs.SetOutput(errOut)
	base := fs.String("api-url", defaultURL, "service HTTP origin")
	format := fs.String("format", "table", "table or json for the compiled method catalogue")
	id := fs.String("id", "", "inspect one method definition and content hash as JSON")
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
		return errors.New("invalid library arguments")
	}
	var c experiment.Catalog
	if err := request(ctx, *base, "/v1/library", nil, &c); err != nil {
		return err
	}
	if err := c.Validate(); err != nil {
		return err
	}
	if *id != "" {
		for _, m := range c.Methods {
			if m.ID == *id {
				return emit(out, m)
			}
		}
		return errors.New("method not found in this service's compiled catalogue")
	}
	if *format == "json" {
		return emit(out, c)
	}
	if err := renderMethodCatalog(out, c); err != nil {
		return err
	}
	_, err = fmt.Fprintln(out, "Use library --id METHOD_ID for exact safety, checks, assumptions and definition hash. Inspection does not execute an experiment or activate a strategy.")
	return err
}

func renderMethodCatalog(out io.Writer, c experiment.Catalog) error {
	if _, err := fmt.Fprintf(out, "SCOPE %s  CONTRACT %s\nCOMPILED SYNTHETIC METHODS (unqualified; not an active portfolio or result history)\nID  KIND  IMPLEMENTATION  EVIDENCE  COST MODEL\n", strconv.Quote(c.Scope), strconv.Quote(c.ContractVersion)); err != nil {
		return err
	}
	for _, m := range c.Methods {
		e := m.Definition.Entry
		if _, err := fmt.Fprintf(out, "%s  %s  %s  %s  %s\n", strconv.Quote(m.ID), strconv.Quote(e.Kind), strconv.Quote(e.Implementation), strconv.Quote(e.EvidenceClass), strconv.Quote(m.Definition.CostModel)); err != nil {
			return err
		}
	}
	return nil
}
