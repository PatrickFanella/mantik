package cli

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"io"

	"git.subcult.tv/subculture-collective/mantik/internal/candidateattempt"
	"git.subcult.tv/subculture-collective/mantik/internal/workerlaunch"
	"git.subcult.tv/subculture-collective/mantik/internal/workerplan"
)

func runRegisteredCandidate(ctx context.Context, command string, args []string, out, errOut io.Writer) error {
	fs := flag.NewFlagSet(command, flag.ContinueOnError)
	fs.SetOutput(errOut)
	root := fs.String("journal", "", "absolute private receiver-bound journal; preserve this owner for retries")
	file := fs.String("intent", "", "exported complete service intent receipt")
	sha := fs.String("intent-sha256", "", "required independently inspected intent identity")
	receiver := fs.String("receiver-id", "", "required expected service receiver identity")
	id := fs.String("request-id", "", "required expected service request identity (at most 80 characters)")
	approved := fs.String("approved-image-sha256", "", "explicit approved immutable behavior image")
	var dataset string
	var cancel bool
	if command == "candidate-worker-run" {
		fs.StringVar(&dataset, "dataset", "", "exact original synthetic dataset")
	} else if command == "candidate-worker-reconcile" {
		fs.BoolVar(&cancel, "cancel", false, "stop only this journal request's original owned handle")
	}
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if fs.NArg() != 0 || *root == "" || *file == "" || *receiver == "" || *id == "" || *approved == "" || !candidateRegistrySHA.MatchString(*sha) {
		return workerlaunch.ErrConflict
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	raw, err := externalFile(*file, candidateattempt.MaxBytes)
	if err != nil {
		return err
	}
	i, err := candidateattempt.ReadIntent(bytes.NewReader(raw))
	if err != nil || i.SHA256 != *sha || i.Command.ReceiverID != *receiver || i.Command.RequestID != *id || i.Command.Job.Plan.Candidate.Toolchain.ImageSHA256 != *approved {
		return workerlaunch.ErrConflict
	}
	if command == "candidate-worker-capture" {
		c, err := workerlaunch.CaptureRegisteredCandidate(ctx, *root, i)
		if err != nil {
			return err
		}
		return emit(out, c)
	}
	var a workerlaunch.CandidateAttempt
	if command == "candidate-worker-run" {
		data, e := externalFile(dataset, workerplan.MaxInputBytes)
		if e != nil {
			return e
		}
		a, err = workerlaunch.RunRegisteredCandidateBehavior(ctx, *root, i, data, *approved)
	} else {
		a, err = workerlaunch.ReconcileRegisteredCandidateRequest(ctx, *root, i, *approved, cancel)
	}
	return emitCandidateAttempt(out, a, err)
}
