package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/url"
	"regexp"
	"strconv"

	"git.subcult.tv/subculture-collective/mantik/internal/domain"
)

func runCases(ctx context.Context, args []string, out, errOut io.Writer) error {
	fs := flag.NewFlagSet("cases", flag.ContinueOnError)
	fs.SetOutput(errOut)
	base := fs.String("api-url", defaultURL, "service HTTP origin")
	id := fs.String("id", "", "inspect one case and its latest retained references as JSON")
	format := fs.String("format", "table", "table or json for the current catalogue")
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
	if fs.NArg() != 0 || (*format != "table" && *format != "json") || *id != "" && !regexp.MustCompile(`^[a-zA-Z0-9_-]{1,128}$`).MatchString(*id) {
		return errors.New("invalid case arguments")
	}
	if *id != "" {
		var detail domain.CaseInspection
		if err := request(ctx, *base, "/v1/cases/"+*id, nil, &detail); err != nil {
			return err
		}
		return emit(out, detail)
	}
	var catalog domain.CaseCatalog
	if err := request(ctx, *base, "/v1/cases", nil, &catalog); err != nil {
		return err
	}
	if *format == "json" {
		return emit(out, catalog)
	}
	if _, err := fmt.Fprintf(out, "MODE %s (unqualified fixture execution)\nCURRENT CASES (on demand; routing capacity 260)\nKIND  ID  STATUS  BEHAVIOR  MATCHES\nfixture_execution  %s  %s  fixture  -\n", strconv.Quote(catalog.Mode), strconv.Quote(catalog.Fixture.ID), strconv.Quote(catalog.Fixture.Status)); err != nil {
		return err
	}
	for _, c := range catalog.Routing {
		if _, err := fmt.Fprintf(out, "routing  %s  %s  %s  %d\n", strconv.Quote(c.ID), strconv.Quote(c.Status), strconv.Quote(c.Behavior), c.MatchCount); err != nil {
			return err
		}
	}
	_, err = fmt.Fprintln(out, "Use cases --id CASE_ID for current structured details and latest retained references. Research routing does not grant execution authority.")
	return err
}
