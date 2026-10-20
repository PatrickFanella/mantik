package cli

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"io"

	"git.subcult.tv/subculture-collective/mantik/internal/candidate"
	"git.subcult.tv/subculture-collective/mantik/internal/candidatebuild"
	"git.subcult.tv/subculture-collective/mantik/internal/workerlaunch"
)

func runCandidateBuild(ctx context.Context, command string, args []string, out, errOut io.Writer) error {
	fs := flag.NewFlagSet(command, flag.ContinueOnError)
	fs.SetOutput(errOut)
	var file, expected, jobPath, jobSHA, root, approved string
	var cancel bool
	switch command {
	case "candidate-build-job":
		fs.StringVar(&file, "candidate", "", "sealed candidate JSON")
		fs.StringVar(&expected, "candidate-sha256", "", "expected candidate identity")
	case "candidate-build-reconcile":
		fs.StringVar(&root, "out-dir", "", "existing private attempt directory")
		fs.StringVar(&approved, "approved-image-sha256", "", "explicit approved compiler image")
		fs.BoolVar(&cancel, "cancel", false, "stop only this owned attempt")
	default:
		fs.StringVar(&jobPath, "job", "", "sealed build job JSON")
		fs.StringVar(&jobSHA, "job-sha256", "", "expected job identity")
		if command == "candidate-build-launch" {
			fs.StringVar(&root, "out-dir", "", "new private attempt directory")
			fs.StringVar(&approved, "approved-image-sha256", "", "explicit approved compiler image")
		}
		if command == "candidate-build-result" {
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
		return candidatebuild.ErrInvalid
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if command == "candidate-build-job" {
		if file == "" || expected == "" {
			return candidatebuild.ErrInvalid
		}
		raw, err := externalFile(file, candidate.MaxPackageBytes)
		if err != nil {
			return err
		}
		p, err := candidate.Read(bytes.NewReader(raw))
		if err != nil || p.SHA256 != expected {
			return candidatebuild.ErrInvalid
		}
		j, err := candidatebuild.New(p)
		if err != nil {
			return err
		}
		return emit(out, j)
	}
	if command == "candidate-build-reconcile" {
		if root == "" || approved == "" {
			return candidatebuild.ErrInvalid
		}
		r, err := workerlaunch.ReconcileCandidateBuild(ctx, root, approved, cancel)
		if err != nil {
			return err
		}
		return emit(out, r)
	}
	if jobPath == "" || jobSHA == "" {
		return candidatebuild.ErrInvalid
	}
	raw, err := externalFile(jobPath, candidatebuild.MaxJobBytes)
	if err != nil {
		return err
	}
	j, err := candidatebuild.ReadJob(bytes.NewReader(raw))
	if err != nil || j.SHA256 != jobSHA {
		return candidatebuild.ErrInvalid
	}
	switch command {
	case "candidate-build-launch":
		if root == "" || approved == "" {
			return candidatebuild.ErrInvalid
		}
		r, err := workerlaunch.LaunchCandidateBuild(ctx, root, j, approved)
		if err != nil {
			return err
		}
		if err := emit(out, r); err != nil {
			return err
		}
		if r.Status != "completed" {
			return errors.New("candidate build did not complete; retained attempt remains inspectable")
		}
		return nil
	case "candidate-build-result":
		if file == "" {
			return candidatebuild.ErrInvalid
		}
		raw, err := externalFile(file, candidatebuild.MaxResultBytes)
		if err != nil {
			return err
		}
		r, err := candidatebuild.ReadResult(bytes.NewReader(raw), j)
		if err != nil {
			return err
		}
		return emit(out, r)
	case "candidate-build-execute":
		// Fixed paths belong to the reviewed container profile. No host paths,
		// compiler flags or commands are accepted from the candidate/job.
		source, err := externalFile("/worker/candidate.go", candidate.MaxSourceBytes)
		if err != nil {
			return err
		}
		harness, err := externalFile("/worker/interface.go", 256)
		if err != nil {
			return err
		}
		r, err := candidatebuild.Execute(ctx, j, source, harness)
		if err != nil {
			return err
		}
		if err := emit(out, r); err != nil {
			return err
		}
		if r.Status != "completed" {
			return errors.New("candidate compile check failed")
		}
		return nil
	}
	return candidatebuild.ErrInvalid
}
