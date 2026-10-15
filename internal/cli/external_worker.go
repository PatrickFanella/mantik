package cli

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"git.subcult.tv/subculture-collective/mantik/internal/externalplan"
	"git.subcult.tv/subculture-collective/mantik/internal/workerlaunch"
	"git.subcult.tv/subculture-collective/mantik/internal/workerplan"
)

func externalFile(path string, limit int64) ([]byte, error) {
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Size() > limit {
		return nil, workerplan.ErrInvalid
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	raw, readErr := io.ReadAll(io.LimitReader(f, limit+1))
	err = errors.Join(readErr, f.Close())
	if err != nil || int64(len(raw)) > limit {
		return nil, workerplan.ErrInvalid
	}
	return raw, nil
}
func externalInputs(root string) (map[string][]byte, error) {
	inputs := map[string][]byte{}
	for _, name := range []string{"dataset.json", "recipe.json", "native.json"} {
		raw, err := externalFile(filepath.Join(root, name), workerplan.MaxInputBytes)
		if err != nil {
			return nil, err
		}
		inputs[name] = raw
	}
	return inputs, nil
}
func runExternalWorker(ctx context.Context, command string, args []string, out, errOut io.Writer) error {
	fs := flag.NewFlagSet(command, flag.ContinueOnError)
	fs.SetOutput(errOut)
	var jobPath, jobSHA, inputDir, root, approved, jobID, image, adapter, requirements, file string
	var cancel bool
	switch command {
	case "external-worker-job":
		fs.StringVar(&jobID, "id", "", "external comparison job identity")
		fs.StringVar(&image, "image-sha256", "", "declared immutable local Python image")
		fs.StringVar(&adapter, "adapter-sha256", "", "reviewed adapter source artifact pin")
		fs.StringVar(&requirements, "requirements-sha256", "", "reviewed dependency lock artifact pin")
		fs.StringVar(&inputDir, "input-dir", "", "directory containing dataset.json, recipe.json and native.json")
	case "external-worker-launch", "external-worker-result":
		fs.StringVar(&jobPath, "job", "", "sealed external worker job")
		fs.StringVar(&jobSHA, "job-sha256", "", "expected external job identity")
		if command == "external-worker-launch" {
			fs.StringVar(&inputDir, "input-dir", "", "directory containing all pinned input artifacts")
			fs.StringVar(&root, "out-dir", "", "new private physical attempt directory")
			fs.StringVar(&approved, "approved-image-sha256", "", "explicit approved immutable local image")
		} else {
			fs.StringVar(&file, "file", "", "exact Python comparison envelope")
		}
	case "external-worker-reconcile":
		fs.StringVar(&root, "out-dir", "", "retained external attempt directory")
		fs.StringVar(&approved, "approved-image-sha256", "", "explicit approved immutable local image")
		fs.BoolVar(&cancel, "cancel", false, "explicitly stop this owned running attempt")
	}
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if fs.NArg() != 0 {
		return workerplan.ErrInvalid
	}
	if command == "external-worker-reconcile" {
		if root == "" || approved == "" {
			return workerplan.ErrInvalid
		}
		r, err := workerlaunch.ReconcileExternal(ctx, root, approved, cancel)
		if err != nil {
			return err
		}
		return emit(out, r)
	}
	if command == "external-worker-job" {
		if inputDir == "" {
			return workerplan.ErrInvalid
		}
		inputs, err := externalInputs(inputDir)
		if err != nil {
			return err
		}
		j, err := externalplan.Declare(jobID, image, adapter, requirements, inputs)
		if err != nil {
			return err
		}
		return emit(out, j)
	}
	if jobPath == "" || jobSHA == "" {
		return workerplan.ErrInvalid
	}
	raw, err := externalFile(jobPath, workerplan.MaxJobBytes)
	if err != nil {
		return err
	}
	j, err := externalplan.ReadJob(bytes.NewReader(raw))
	if err != nil || j.SHA256 != jobSHA {
		return workerplan.ErrInvalid
	}
	if command == "external-worker-result" {
		if file == "" {
			return workerplan.ErrInvalid
		}
		raw, err := externalFile(file, externalplan.MaxResultBytes)
		if err != nil {
			return err
		}
		if _, err = externalplan.ReadResult(bytes.NewReader(raw), j); err != nil {
			return err
		}
		// Preserve the producer's compact bytes and hash, rather than reformatting.
		_, err = out.Write(raw)
		return err
	}
	if root == "" || approved == "" || inputDir == "" {
		return workerplan.ErrInvalid
	}
	inputs, err := externalInputs(inputDir)
	if err != nil {
		return err
	}
	r, err := workerlaunch.LaunchExternal(ctx, root, j, inputs, approved)
	if err != nil {
		return err
	}
	if err = emit(out, r); err != nil {
		return err
	}
	if r.Status != "completed" {
		return fmt.Errorf("external worker %s: %s (attempt retained)", r.Status, r.ReasonCode)
	}
	return nil
}
