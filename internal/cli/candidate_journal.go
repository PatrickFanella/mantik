package cli

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"io"

	"git.subcult.tv/subculture-collective/mantik/internal/candidateexec"
	"git.subcult.tv/subculture-collective/mantik/internal/workerlaunch"
	"git.subcult.tv/subculture-collective/mantik/internal/workerplan"
)

func runCandidateJournal(ctx context.Context, command string, args []string, out, errOut io.Writer) error {
	fs := flag.NewFlagSet(command, flag.ContinueOnError)
	fs.SetOutput(errOut)
	var root, id, jobPath, jobSHA, dataset, approved string
	var cancel bool
	fs.StringVar(&root, "journal", "", "absolute private candidate attempt journal")
	if command != "candidate-attempt-history" {
		fs.StringVar(&id, "request-id", "", "journal-scoped idempotency identity")
		fs.StringVar(&approved, "approved-image-sha256", "", "explicit approved behavior image")
	}
	if command == "candidate-attempt-run" {
		fs.StringVar(&jobPath, "job", "", "sealed behavior job")
		fs.StringVar(&jobSHA, "job-sha256", "", "expected job identity")
		fs.StringVar(&dataset, "dataset", "", "exact original synthetic dataset")
	}
	if command == "candidate-attempt-reconcile" {
		fs.BoolVar(&cancel, "cancel", false, "stop only this journal request's owned handle")
	}
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if fs.NArg() != 0 || root == "" {
		return workerlaunch.ErrConflict
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if command == "candidate-attempt-history" {
		h, err := workerlaunch.CandidateBehaviorHistory(ctx, root)
		if err != nil {
			return err
		}
		return emit(out, h)
	}
	if id == "" || approved == "" {
		return workerlaunch.ErrConflict
	}
	var a workerlaunch.CandidateAttempt
	var err error
	if command == "candidate-attempt-run" {
		if jobPath == "" || jobSHA == "" || dataset == "" {
			return workerlaunch.ErrConflict
		}
		raw, e := externalFile(jobPath, candidateexec.MaxJobBytes)
		if e != nil {
			return e
		}
		j, e := candidateexec.ReadJob(bytes.NewReader(raw))
		if e != nil || j.SHA256 != jobSHA {
			return workerlaunch.ErrConflict
		}
		data, e := externalFile(dataset, workerplan.MaxInputBytes)
		if e != nil {
			return e
		}
		a, err = workerlaunch.RunCandidateBehavior(ctx, root, id, j, data, approved)
	} else {
		a, err = workerlaunch.ReconcileCandidateRequest(ctx, root, id, approved, cancel)
	}
	// If execution or collection is unresolved, still show the retained intent.
	// This is not a statement that its original handle is live or stopped.
	if a.Intent.SHA256 != "" {
		if e := emit(out, a); e != nil {
			return e
		}
	}
	if err != nil {
		return err
	}
	if a.Outcome == nil || a.Outcome.Receipt.Status != "completed" {
		return errors.New("candidate attempt did not complete with matching annotations; inspect retained journal evidence")
	}
	return nil
}
