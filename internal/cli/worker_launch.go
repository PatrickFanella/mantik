package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"

	"git.subcult.tv/subculture-collective/mantik/internal/workerlaunch"
	"git.subcult.tv/subculture-collective/mantik/internal/workerplan"
)

func runWorkerLaunch(ctx context.Context, command string, args []string, out, errOut io.Writer) error {
	fs := flag.NewFlagSet(command, flag.ContinueOnError)
	fs.SetOutput(errOut)
	root := fs.String("out-dir", "", "private attempt directory; must be new for launch")
	approved := fs.String("approved-image-sha256", "", "explicit approved local image identity")
	var jobPath, jobSHA, input string
	var cancel bool
	if command == "worker-launch" {
		fs.StringVar(&jobPath, "job", "", "sealed worker job")
		fs.StringVar(&jobSHA, "job-sha256", "", "expected job identity")
		fs.StringVar(&input, "input", "", "pinned dataset artifact")
	} else {
		fs.BoolVar(&cancel, "cancel", false, "explicitly cancel this owned running attempt")
	}
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if fs.NArg() != 0 || *root == "" || *approved == "" {
		return workerplan.ErrInvalid
	}
	if command == "worker-reconcile" {
		r, err := workerlaunch.Reconcile(ctx, *root, *approved, cancel)
		if err != nil {
			return err
		}
		return emit(out, r)
	}
	if jobPath == "" || jobSHA == "" || input == "" {
		return workerplan.ErrInvalid
	}
	f, err := os.Open(jobPath)
	if err != nil {
		return errors.New("cannot open worker job")
	}
	j, readErr := workerplan.ReadJob(f)
	err = errors.Join(readErr, f.Close())
	if err != nil {
		return err
	}
	if j.SHA256 != jobSHA {
		return errors.New("worker job identity mismatch")
	}
	info, err := os.Lstat(input)
	if err != nil || !info.Mode().IsRegular() {
		return errors.New("worker input must be a regular file")
	}
	f, err = os.Open(input)
	if err != nil {
		return errors.New("cannot open worker input")
	}
	r, runErr := workerlaunch.Launch(ctx, *root, j, f, *approved)
	err = errors.Join(runErr, f.Close())
	if err != nil {
		return err
	}
	if err = emit(out, r); err != nil {
		return err
	}
	if r.Status != "completed" {
		return fmt.Errorf("worker launch %s: %s (attempt retained)", r.Status, r.ReasonCode)
	}
	return nil
}
