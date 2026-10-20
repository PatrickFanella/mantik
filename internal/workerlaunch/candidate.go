package workerlaunch

import (
	"bytes"
	"context"
	"encoding/json"

	"git.subcult.tv/subculture-collective/mantik/internal/candidate"
	"git.subcult.tv/subculture-collective/mantik/internal/candidatebuild"
)

func candidateExecution(j candidatebuild.Job) execution {
	raw, _ := json.Marshal(j)
	return execution{raw: raw, sha: j.SHA256, image: j.Candidate.Toolchain.ImageSHA256, resources: j.Resources,
		acquisitionSchema: "mantik-candidate-build-acquisition-v1", receiptSchema: "mantik-candidate-build-host-receipt-v1", authority: "candidate_compile_host_receipt_only",
		entrypoint: []string{"/app/mantik"}, args: []string{"candidate-build-execute", "--job", "/worker/job.json", "--job-sha256", j.SHA256}, env: []string{"PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin"},
		outcome: func(raw []byte) (outcome, error) {
			r, err := candidatebuild.ReadResult(bytes.NewReader(raw), j)
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

func LaunchCandidateBuild(ctx context.Context, root string, j candidatebuild.Job, approved string) (Receipt, error) {
	return launchCandidateBuild(ctx, root, j, approved, nil)
}
func launchCandidateBuild(ctx context.Context, root string, j candidatebuild.Job, approved string, d driver) (Receipt, error) {
	if j.Validate() != nil || approved != j.Candidate.Toolchain.ImageSHA256 {
		return Receipt{}, ErrConflict
	}
	return launchExecution(ctx, root, candidateExecution(j), j.Inputs(), approved, d)
}

func ReconcileCandidateBuild(ctx context.Context, root, approved string, cancel bool) (Receipt, error) {
	return reconcileCandidateBuild(ctx, root, approved, cancel, nil)
}
func reconcileCandidateBuild(ctx context.Context, root, approved string, cancel bool, d driver) (Receipt, error) {
	root, err := directory(root, false)
	if err != nil {
		return Receipt{}, err
	}
	unlock, err := lock(ctx, root)
	if err != nil {
		return Receipt{}, err
	}
	defer unlock()
	a, j, err := loadCandidateBuild(root, approved)
	if err != nil {
		return Receipt{}, err
	}
	p := candidateExecution(j)
	reason := ""
	if cancel {
		reason = "cancelled"
	}
	if d == nil {
		d = dockerCLI{root + "/docker-config"}
	}
	return collectExecution(root, a, p, d, reason, false)
}
func loadCandidateBuild(root, approved string) (Acquisition, candidatebuild.Job, error) {
	raw, err := readFile(root, "job.json", candidatebuild.MaxJobBytes)
	if err != nil {
		return Acquisition{}, candidatebuild.Job{}, err
	}
	j, err := candidatebuild.ReadJob(bytes.NewReader(raw))
	if err != nil {
		return Acquisition{}, j, err
	}
	inputJob, err := readFile(root+"/input", "job.json", candidatebuild.MaxJobBytes)
	if err != nil || !bytes.Equal(raw, inputJob) {
		return Acquisition{}, j, ErrConflict
	}
	source, err := readFile(root+"/input", "candidate.go", candidate.MaxSourceBytes)
	if err != nil {
		return Acquisition{}, j, err
	}
	harness, err := readFile(root+"/input", "interface.go", 256)
	if err != nil {
		return Acquisition{}, j, err
	}
	if !candidatebuild.ValidInputs(j, source, harness) {
		return Acquisition{}, j, ErrConflict
	}
	raw, err = readFile(root, "acquisition.json", 8192)
	if err != nil {
		return Acquisition{}, j, err
	}
	var a Acquisition
	p := candidateExecution(j)
	if json.Unmarshal(raw, &a) != nil || a.Validate(root, receiptProfile(p), approved) != nil {
		return Acquisition{}, j, ErrConflict
	}
	return a, j, nil
}
