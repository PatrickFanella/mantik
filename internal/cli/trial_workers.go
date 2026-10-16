package cli

import (
	"context"
	"errors"
	"flag"
	"io"
	"net/url"

	"git.subcult.tv/subculture-collective/mantik/internal/review"
	"git.subcult.tv/subculture-collective/mantik/internal/trial"
	"git.subcult.tv/subculture-collective/mantik/internal/workertrial"
)

func runTrialWorker(ctx context.Context, command string, args []string, out, errOut io.Writer) error {
	fs := flag.NewFlagSet(command, flag.ContinueOnError)
	fs.SetOutput(errOut)
	base := fs.String("api-url", defaultURL, "service HTTP origin")
	id := fs.String("id", "", "external job request ID")
	var c workertrial.Command
	var name string
	var raw, jobOnly bool
	if command == "trial-worker-declare" {
		fs.StringVar(&c.ReceiverID, "receiver-id", "", "explicit pinned research receiver")
		fs.StringVar(&c.AttemptID, "attempt", "", "completed native service attempt ID")
		fs.StringVar(&c.NativeRunSHA256, "native-run-sha256", "", "expected retained native-run receipt identity")
		fs.StringVar(&c.ImageSHA256, "image-sha256", "", "declared immutable external image")
		fs.StringVar(&c.AdapterSHA256, "adapter-sha256", "", "reviewed adapter source identity")
		fs.StringVar(&c.RequirementsSHA256, "requirements-sha256", "", "reviewed dependency lock identity")
	} else if command == "trial-worker-input" {
		fs.StringVar(&name, "name", "", "dataset.json, recipe.json or native.json")
		fs.BoolVar(&raw, "raw", false, "return exact input bytes instead of artifact wrapper")
	} else {
		fs.BoolVar(&jobOnly, "job-only", false, "return the sealed external job for local launch")
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
	path := "/v1/external-trial-jobs/" + *id
	if command == "trial-worker-input" {
		if name != "dataset.json" && name != "recipe.json" && name != "native.json" {
			return trial.ErrInvalid
		}
		var a workertrial.Artifact
		if err = requestWithQuery(ctx, *base, path+"/inputs/"+name, nil, &a, nil, 32<<20); err != nil {
			return err
		}
		// Bind the targeted artifact to a separately validated retained declaration.
		var r workertrial.Record
		if err = request(ctx, *base, path, nil, &r); err != nil {
			return err
		}
		if r.Command.RequestID != *id || a.ID != *id || a.Reference != name {
			return trial.ErrInvalid
		}
		checked, err := workertrial.Input(r, name, a.Content)
		if err != nil || checked.SHA256 != a.SHA256 {
			return trial.ErrInvalid
		}
		if raw {
			_, err = out.Write([]byte(a.Content))
			return err
		}
		return emit(out, a)
	}
	var body any
	if command == "trial-worker-declare" {
		c.RequestID = *id
		if c.Validate() != nil {
			return trial.ErrInvalid
		}
		body = c
		path = "/v1/external-trial-jobs"
	}
	var r workertrial.Record
	if err = request(ctx, *base, path, body, &r); err != nil {
		return err
	}
	if r.Validate() != nil || r.Command.RequestID != *id || command == "trial-worker-declare" && r.Command != c {
		return trial.ErrInvalid
	}
	if jobOnly {
		return emit(out, r.Job)
	}
	return emit(out, r)
}
