package workerlaunch

import (
	"bytes"
	"context"
	"encoding/json"

	"git.subcult.tv/subculture-collective/mantik/internal/candidate"
	"git.subcult.tv/subculture-collective/mantik/internal/candidateevidence"
	"git.subcult.tv/subculture-collective/mantik/internal/candidateexec"
	"git.subcult.tv/subculture-collective/mantik/internal/workerplan"
)

func behaviorExecution(j candidateexec.Job) execution {
	raw, _ := json.Marshal(j)
	profile := candidateevidence.BehaviorProfile(j)
	return execution{raw: raw, sha: j.SHA256, image: j.Plan.Candidate.Toolchain.ImageSHA256, resources: j.Resources, behaviorTmp: true,
		acquisitionSchema: "mantik-candidate-behavior-acquisition-v1", receiptSchema: "mantik-candidate-behavior-host-receipt-v1", authority: "synthetic_candidate_behavior_host_receipt_only",
		entrypoint: profile.Entrypoint, args: profile.Args, env: profile.Env,
		outcome: func(raw []byte) (outcome, error) {
			r, err := candidateexec.ReadResult(bytes.NewReader(raw), j)
			if err != nil {
				return outcome{}, err
			}
			o := outcome{sha: r.SHA256, workerStatus: r.Status, status: r.Status, reason: r.ReasonCode, exit: 1, eligible: true}
			if r.Status == "completed" {
				o.exit = 0
			}
			return o, nil
		}}
}

func LaunchCandidateBehavior(ctx context.Context, root string, j candidateexec.Job, dataset []byte, approved string) (Receipt, error) {
	return launchCandidateBehavior(ctx, root, j, dataset, approved, nil)
}
func launchCandidateBehavior(ctx context.Context, root string, j candidateexec.Job, dataset []byte, approved string, d driver) (Receipt, error) {
	if j.Validate() != nil || j.Plan.VerifyDataset(dataset) != nil || approved != j.Plan.Candidate.Toolchain.ImageSHA256 {
		return Receipt{}, ErrConflict
	}
	return launchExecution(ctx, root, behaviorExecution(j), j.Inputs(dataset), approved, d)
}

func ReconcileCandidateBehavior(ctx context.Context, root, approved string, cancel bool) (Receipt, error) {
	return reconcileCandidateBehavior(ctx, root, approved, cancel, nil)
}
func reconcileCandidateBehavior(ctx context.Context, root, approved string, cancel bool, d driver) (Receipt, error) {
	root, err := directory(root, false)
	if err != nil {
		return Receipt{}, err
	}
	unlock, err := lock(ctx, root)
	if err != nil {
		return Receipt{}, err
	}
	defer unlock()
	a, j, err := loadCandidateBehavior(root, approved)
	if err != nil {
		return Receipt{}, err
	}
	p := behaviorExecution(j)
	reason := ""
	if cancel {
		reason = "cancelled"
	}
	if d == nil {
		d = dockerCLI{root + "/docker-config"}
	}
	return collectExecution(root, a, p, d, reason, false)
}
func loadCandidateBehavior(root, approved string) (Acquisition, candidateexec.Job, error) {
	raw, err := readFile(root, "job.json", candidateexec.MaxJobBytes)
	if err != nil {
		return Acquisition{}, candidateexec.Job{}, err
	}
	j, err := candidateexec.ReadJob(bytes.NewReader(raw))
	if err != nil {
		return Acquisition{}, j, err
	}
	inputJob, err := readFile(root+"/input", "job.json", candidateexec.MaxJobBytes)
	if err != nil || !bytes.Equal(raw, inputJob) {
		return Acquisition{}, j, ErrConflict
	}
	source, err := readFile(root+"/input", "candidate.go", candidate.MaxSourceBytes)
	if err != nil {
		return Acquisition{}, j, err
	}
	harness, err := readFile(root+"/input", "harness.go", 16384)
	if err != nil {
		return Acquisition{}, j, err
	}
	dataset, err := readFile(root+"/input", "dataset.json", workerplan.MaxInputBytes)
	if err != nil || j.Plan.VerifyDataset(dataset) != nil || string(source) != j.Plan.Candidate.Source.Text || string(harness) != candidateexec.Harness(j.Plan) {
		return Acquisition{}, j, ErrConflict
	}
	raw, err = readFile(root, "acquisition.json", 8192)
	if err != nil {
		return Acquisition{}, j, err
	}
	var a Acquisition
	p := behaviorExecution(j)
	if json.Unmarshal(raw, &a) != nil || a.Validate(root, receiptProfile(p), approved) != nil {
		return Acquisition{}, j, ErrConflict
	}
	return a, j, nil
}
