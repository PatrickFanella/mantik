package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/url"
	"strconv"

	"git.subcult.tv/subculture-collective/mantik/internal/domain"
	"git.subcult.tv/subculture-collective/mantik/internal/storage"
)

func runExceptions(ctx context.Context, args []string, out, errOut io.Writer) error {
	fs := flag.NewFlagSet("exceptions", flag.ContinueOnError)
	fs.SetOutput(errOut)
	base := fs.String("api-url", defaultURL, "service HTTP origin")
	after := fs.String("after", "", "exclusive exception identity cursor")
	limit := fs.Int("limit", 25, "records per page, 1..100")
	format := fs.String("format", "table", "table or json with retained evidence/recovery records")
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
		return errors.New("invalid exception arguments")
	}
	q := storage.ExceptionQuery{After: *after, Limit: *limit}
	if err := q.Validate(); err != nil {
		return err
	}
	v := url.Values{"limit": {strconv.Itoa(*limit)}}
	if *after != "" {
		v.Set("after", *after)
	}
	var page domain.ExceptionPage
	if err := requestWithQuery(ctx, *base, "/v1/exceptions", nil, &page, v, 8<<20); err != nil {
		return err
	}
	if *format == "json" {
		return emit(out, page)
	}
	if err := renderExceptionPage(out, page); err != nil {
		return err
	}
	_, err = fmt.Fprintln(out, "Use --format json for evidence references and recovery history; use readiness for a fresh capability assessment.")
	return err
}

func renderExceptionPage(out io.Writer, page domain.ExceptionPage) error {
	if _, err := fmt.Fprintf(out, "MODE %s  SCOPE %s\nRETAINED RECORDS ONLY (not a current readiness assessment)\nNEXT AFTER %s  MORE %t\nID  CAPABILITY  SEVERITY  STATUS  CODE  RECOVERY ATTEMPTS\n", strconv.Quote(page.Mode), strconv.Quote(page.Scope), strconv.Quote(page.NextAfter), page.HasMore); err != nil {
		return err
	}
	for _, e := range page.Items {
		if _, err := fmt.Fprintf(out, "%s  %s  %s  %s  %s  %d\n", strconv.Quote(e.ID), strconv.Quote(e.Capability), strconv.Quote(e.Severity), strconv.Quote(e.Status), strconv.Quote(e.Code), len(e.RecoveryAttempts)); err != nil {
			return err
		}
	}
	return nil
}
