package cli

import (
	"context"
	"errors"
	"flag"
	"io"
	"net/url"
	"os"

	"git.subcult.tv/subculture-collective/mantik/internal/review"
	"git.subcult.tv/subculture-collective/mantik/internal/trial"
	"git.subcult.tv/subculture-collective/mantik/internal/workertrial"
)

func runExternalAttempt(ctx context.Context, command string, args []string, out, errOut io.Writer) error {
	fs := flag.NewFlagSet(command, flag.ContinueOnError)
	fs.SetOutput(errOut)
	base := fs.String("api-url", defaultURL, "service HTTP origin")
	id := fs.String("id", "", "external attempt ID (job ID for history)")
	var c workertrial.AttemptCommand
	var report workertrial.ReportCommand
	var stdout, stderr, name string
	var raw bool
	switch command {
	case "trial-worker-attempt-start":
		fs.StringVar(&c.ReceiverID, "receiver-id", "", "pinned research receiver")
		fs.StringVar(&c.JobID, "job", "", "retained external job ID")
		fs.StringVar(&c.DeclarationSHA256, "declaration-sha256", "", "expected retained declaration SHA-256")
	case "trial-worker-attempt-report":
		fs.StringVar(&report.ReceiverID, "receiver-id", "", "pinned research receiver")
		fs.StringVar(&report.IntentSHA256, "intent-sha256", "", "expected immutable attempt intent SHA-256")
		fs.StringVar(&report.ReportedStatus, "status", "", "caller-reported terminal disposition")
		fs.StringVar(&stdout, "stdout-file", "", "exact retained stdout regular file")
		fs.StringVar(&stderr, "stderr-file", "", "exact retained stderr regular file")
	case "trial-worker-attempt-output":
		fs.StringVar(&name, "name", "", "stdout or stderr")
		fs.BoolVar(&raw, "raw", false, "return exact output bytes")
	}
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	u, err := url.Parse(*base)
	if err != nil || u.Scheme != "http" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Path != "" && u.Path != "/" || fs.NArg() != 0 || !review.ID.MatchString(*id) {
		return trial.ErrInvalid
	}
	path := "/v1/external-trial-attempts/" + *id
	switch command {
	case "trial-worker-attempt-start":
		c.RequestID = *id
		if c.Validate() != nil {
			return trial.ErrInvalid
		}
		var i workertrial.Intent
		if err = request(ctx, *base, "/v1/external-trial-attempts", c, &i); err != nil {
			return err
		}
		if i.Validate() != nil || i.Command != c {
			return trial.ErrInvalid
		}
		return emit(out, i)
	case "trial-worker-attempt-report":
		report.AttemptID = *id
		report.Stdout, err = externalTextFile(stdout, workertrial.MaxStdout)
		if err != nil {
			return err
		}
		report.Stderr, err = externalTextFile(stderr, workertrial.MaxStderr)
		if err != nil {
			return err
		}
		if report.Validate() != nil {
			return trial.ErrInvalid
		}
		var a workertrial.Attempt
		if err = request(ctx, *base, path, nil, &a); err != nil {
			return err
		}
		if a.Validate() != nil || a.Intent.Command.RequestID != *id || a.Intent.Command.ReceiverID != report.ReceiverID || a.Intent.SHA256 != report.IntentSHA256 {
			return trial.ErrInvalid
		}
		var o workertrial.Outcome
		if err = request(ctx, *base, path+"/report", report, &o); err != nil {
			return err
		}
		if o.Validate(a.Intent) != nil || o.RequestSHA256 != trial.Hash(report) {
			return trial.ErrInvalid
		}
		return emit(out, o)
	case "trial-worker-attempts":
		var all []workertrial.Attempt
		if err = request(ctx, *base, "/v1/external-trial-jobs/"+*id+"/attempts", nil, &all); err != nil {
			return err
		}
		if all == nil || len(all) > workertrial.MaxAttempts {
			return trial.ErrInvalid
		}
		last := ""
		for _, a := range all {
			if a.Validate() != nil || a.Intent.Command.JobID != *id || a.Intent.Command.RequestID <= last {
				return trial.ErrInvalid
			}
			last = a.Intent.Command.RequestID
		}
		return emit(out, all)
	case "trial-worker-attempt-output":
		if name != "stdout" && name != "stderr" {
			return trial.ErrInvalid
		}
		var body workertrial.Artifact
		if err = requestWithQuery(ctx, *base, path+"/outputs/"+name, nil, &body, nil, workertrial.MaxReportBytes); err != nil {
			return err
		}
		var a workertrial.Attempt
		if err = request(ctx, *base, path, nil, &a); err != nil {
			return err
		}
		if a.Intent.Command.RequestID != *id || body.ID != *id || body.Reference != name {
			return trial.ErrInvalid
		}
		checked, err := workertrial.Output(a, name, body.Content)
		if err != nil || checked.SHA256 != body.SHA256 {
			return trial.ErrInvalid
		}
		if raw {
			_, err = io.WriteString(out, body.Content)
			return err
		}
		return emit(out, body)
	default:
		var a workertrial.Attempt
		if err = request(ctx, *base, path, nil, &a); err != nil {
			return err
		}
		if a.Validate() != nil || a.Intent.Command.RequestID != *id {
			return trial.ErrInvalid
		}
		return emit(out, a)
	}
}
func externalTextFile(path string, limit int) (string, error) {
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Size() > int64(limit) {
		return "", trial.ErrInvalid
	}
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	info, err = f.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() > int64(limit) {
		return "", trial.ErrInvalid
	}
	body, err := io.ReadAll(io.LimitReader(f, int64(limit)+1))
	if err != nil || len(body) > limit {
		return "", trial.ErrInvalid
	}
	return string(body), nil
}
