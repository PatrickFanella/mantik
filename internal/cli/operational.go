package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/url"
	"slices"

	"git.subcult.tv/subculture-collective/mantik/internal/domain"
	"git.subcult.tv/subculture-collective/mantik/internal/operational"
)

func runOperational(ctx context.Context, command string, args []string, out, errOut io.Writer) error {
	fs := flag.NewFlagSet(command, flag.ContinueOnError)
	fs.SetOutput(errOut)
	apiURL := fs.String("api-url", defaultURL, "service HTTP origin")
	format, scope, id, requestID, operation := new(string), new(string), new(string), new(string), new(string)
	*format, *scope, *operation = "table", "storage", "recheck"
	if command != "recover" {
		fs.StringVar(format, "format", "table", "table or json")
	}
	if command == "readiness" {
		fs.StringVar(scope, "scope", "storage", "readiness capability")
	}
	if command == "recover" {
		fs.StringVar(id, "exception", "", "exception id for recovery")
		fs.StringVar(requestID, "request-id", "", "stable request identity for recovery")
		fs.StringVar(operation, "operation", "recheck", "recheck, reconcile-action, or retry-protection")
	}
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if fs.NArg() != 0 || !slices.Contains([]string{"table", "json"}, *format) {
		return errors.New("invalid operational arguments")
	}
	u, err := url.Parse(*apiURL)
	if err != nil || u.Scheme != "http" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Path != "" && u.Path != "/" {
		return errors.New("api-url must be an HTTP origin without credentials or a path")
	}
	switch command {
	case "readiness":
		if !slices.Contains(operational.Scopes, *scope) {
			return errors.New("unknown readiness scope")
		}
		var report domain.ReadinessReport
		if err := request(ctx, *apiURL, "/v1/readiness/"+*scope, nil, &report); err != nil {
			return err
		}
		if *format == "json" {
			return emit(out, report)
		}
		if _, err := fmt.Fprintf(out, "SCOPE  %s\nREADY  %t\n", report.Scope, report.Ready); err != nil {
			return err
		}
		for _, name := range operational.Scopes {
			c := report.Capabilities[name]
			if _, err := fmt.Fprintf(out, "%-24s %-16s %s\n", name, c.Status, c.Code); err != nil {
				return err
			}
		}
		return nil
	case "operations":
		var snapshot domain.OperationsSnapshot
		if err := request(ctx, *apiURL, "/v1/operations", nil, &snapshot); err != nil {
			return err
		}
		if *format == "json" {
			return emit(out, snapshot)
		}
		if _, err := fmt.Fprintln(out, "ID  CAPABILITY  SEVERITY  STATUS  CODE  RECOVERY ATTEMPTS"); err != nil {
			return err
		}
		for _, e := range snapshot.Exceptions {
			if _, err := fmt.Fprintf(out, "%s  %s  %s  %s  %s  %d\n", e.ID, e.Capability, e.Severity, e.Status, e.Code, len(e.RecoveryAttempts)); err != nil {
				return err
			}
		}
		return nil
	case "recover":
		if *id == "" || url.PathEscape(*id) != *id || *requestID == "" || !slices.Contains([]string{"recheck", "reconcile-action", "retry-protection"}, *operation) {
			return errors.New("recovery requires exception, request-id, and supported operation")
		}
		body := struct {
			RequestID string `json:"request_id"`
			Operation string `json:"operation"`
		}{*requestID, *operation}
		var receipt domain.RecoveryReceipt
		if err := request(ctx, *apiURL, "/v1/operations/exceptions/"+*id+"/recover", body, &receipt); err != nil {
			return err
		}
		return emit(out, receipt)
	}
	return errors.New("unsupported operational command")
}
