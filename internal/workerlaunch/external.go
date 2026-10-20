package workerlaunch

import (
	"bytes"
	"context"
	"encoding/json"

	"git.subcult.tv/subculture-collective/mantik/internal/externalplan"
	"git.subcult.tv/subculture-collective/mantik/internal/workerplan"
)

func LaunchExternal(ctx context.Context, root string, j externalplan.Job, inputs map[string][]byte, approved string) (Receipt, error) {
	return launchExternal(ctx, root, j, inputs, approved, nil)
}
func launchExternal(ctx context.Context, root string, j externalplan.Job, inputs map[string][]byte, approved string, d driver) (Receipt, error) {
	if ctx.Err() != nil {
		return Receipt{}, ctx.Err()
	}
	if approved != j.ImageSHA256 || j.ValidateInputs(inputs) != nil {
		return Receipt{}, ErrConflict
	}
	return launchExecution(ctx, root, externalExecution(j), inputs, approved, d)
}
func loadExternal(root, approved string) (Acquisition, externalplan.Job, error) {
	raw, err := readFile(root, "job.json", workerplan.MaxJobBytes)
	if err != nil {
		return Acquisition{}, externalplan.Job{}, err
	}
	j, err := externalplan.ReadJob(bytes.NewReader(raw))
	if err != nil {
		return Acquisition{}, j, err
	}
	inputs := map[string][]byte{}
	for _, artifact := range j.Inputs {
		raw, err := readFile(root+"/input", artifact.Reference, workerplan.MaxInputBytes)
		if err != nil {
			return Acquisition{}, j, err
		}
		inputs[artifact.Reference] = raw
	}
	if j.ValidateInputs(inputs) != nil {
		return Acquisition{}, j, ErrConflict
	}
	raw, err = readFile(root, "acquisition.json", 8192)
	if err != nil {
		return Acquisition{}, j, err
	}
	var a Acquisition
	if json.Unmarshal(raw, &a) != nil || a.Validate(root, receiptProfile(externalExecution(j)), approved) != nil {
		return a, j, ErrConflict
	}
	return a, j, nil
}
func ReconcileExternal(ctx context.Context, root, approved string, cancel bool) (Receipt, error) {
	return reconcileExternal(ctx, root, approved, cancel, nil)
}
func reconcileExternal(ctx context.Context, root, approved string, cancel bool, d driver) (Receipt, error) {
	root, err := directory(root, false)
	if err != nil {
		return Receipt{}, err
	}
	unlock, err := lock(ctx, root)
	if err != nil {
		return Receipt{}, err
	}
	defer unlock()
	a, j, err := loadExternal(root, approved)
	if err != nil {
		return Receipt{}, err
	}
	if d == nil {
		d = dockerCLI{root + "/docker-config"}
	}
	reason := ""
	if cancel {
		reason = "cancelled"
	}
	return collectExecution(root, a, externalExecution(j), d, reason, false)
}
