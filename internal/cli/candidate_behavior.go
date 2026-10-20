package cli

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"io"

	"git.subcult.tv/subculture-collective/mantik/internal/candidate"
	"git.subcult.tv/subculture-collective/mantik/internal/candidateexec"
	"git.subcult.tv/subculture-collective/mantik/internal/candidatetest"
	"git.subcult.tv/subculture-collective/mantik/internal/workerlaunch"
	"git.subcult.tv/subculture-collective/mantik/internal/workerplan"
)

func runCandidateBehavior(ctx context.Context, command string, args []string, out, errOut io.Writer) error {
	fs := flag.NewFlagSet(command, flag.ContinueOnError)
	fs.SetOutput(errOut)
	var file, expected, jobPath, jobSHA, root, approved string
	var cancel bool
	var dataset string
	if command == "candidate-behavior-job" || command == "candidate-behavior-launch" {
		fs.StringVar(&dataset, "dataset", "", "exact pinned synthetic dataset")
	}
	switch command {
	case "candidate-behavior-job":
		fs.StringVar(&file, "plan", "", "sealed fixture test plan")
		fs.StringVar(&expected, "plan-sha256", "", "expected fixture plan identity")
	case "candidate-behavior-reconcile":
		fs.StringVar(&root, "out-dir", "", "existing private attempt directory")
		fs.StringVar(&approved, "approved-image-sha256", "", "explicit approved behavior image")
		fs.BoolVar(&cancel, "cancel", false, "stop only this owned attempt")
	default:
		fs.StringVar(&jobPath, "job", "", "sealed behavior job JSON")
		fs.StringVar(&jobSHA, "job-sha256", "", "expected job identity")
		if command == "candidate-behavior-launch" {
			fs.StringVar(&root, "out-dir", "", "new private attempt directory")
			fs.StringVar(&approved, "approved-image-sha256", "", "explicit approved behavior image")
		}
		if command == "candidate-behavior-result" {
			fs.StringVar(&file, "file", "", "retained worker result JSON")
		}
	}
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if fs.NArg() != 0 {
		return candidateexec.ErrInvalid
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if command == "candidate-behavior-job" {
		if file == "" || expected == "" {
			return candidateexec.ErrInvalid
		}
		raw, err := externalFile(file, candidatetest.MaxPlanBytes)
		if err != nil {
			return err
		}
		p, err := candidatetest.ReadPlan(bytes.NewReader(raw))
		if err != nil || p.SHA256 != expected {
			return candidateexec.ErrInvalid
		}
		data, err := externalFile(dataset, workerplan.MaxInputBytes)
		if err != nil {
			return err
		}
		j, err := candidateexec.New(p, data)
		if err != nil {
			return err
		}
		return emit(out, j)
	}
	if command == "candidate-behavior-reconcile" {
		if root == "" || approved == "" {
			return candidateexec.ErrInvalid
		}
		r, err := workerlaunch.ReconcileCandidateBehavior(ctx, root, approved, cancel)
		if err != nil {
			return err
		}
		return emit(out, r)
	}
	if jobPath == "" || jobSHA == "" {
		return candidateexec.ErrInvalid
	}
	raw, err := externalFile(jobPath, candidateexec.MaxJobBytes)
	if err != nil {
		return err
	}
	j, err := candidateexec.ReadJob(bytes.NewReader(raw))
	if err != nil || j.SHA256 != jobSHA {
		return candidateexec.ErrInvalid
	}
	switch command {
	case "candidate-behavior-launch":
		if root == "" || approved == "" {
			return candidateexec.ErrInvalid
		}
		data, err := externalFile(dataset, workerplan.MaxInputBytes)
		if err != nil {
			return err
		}
		r, err := workerlaunch.LaunchCandidateBehavior(ctx, root, j, data, approved)
		if err != nil {
			return err
		}
		if err := emit(out, r); err != nil {
			return err
		}
		if r.Status != "completed" {
			return errors.New("candidate behavior did not match every annotation; retained attempt remains inspectable")
		}
		return nil
	case "candidate-behavior-result":
		if file == "" {
			return candidateexec.ErrInvalid
		}
		raw, err := externalFile(file, candidateexec.MaxResultBytes)
		if err != nil {
			return err
		}
		r, err := candidateexec.ReadResult(bytes.NewReader(raw), j)
		if err != nil {
			return err
		}
		return emit(out, r)
	case "candidate-behavior-execute":
		// Fixed paths and commands belong to the dedicated container profile.
		source, err := externalFile("/worker/candidate.go", candidate.MaxSourceBytes)
		if err != nil {
			return err
		}
		harness, err := externalFile("/worker/harness.go", 16384)
		if err != nil {
			return err
		}
		data, err := externalFile("/worker/dataset.json", workerplan.MaxInputBytes)
		if err != nil {
			return err
		}
		r, err := candidateexec.Execute(ctx, j, data, source, harness)
		if err != nil {
			return err
		}
		if err := emit(out, r); err != nil {
			return err
		}
		if r.Status != "completed" {
			return errors.New("candidate behavior failed or mismatched")
		}
		return nil
	}
	return candidateexec.ErrInvalid
}
