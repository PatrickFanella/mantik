package workerlaunch

import (
	"bytes"
	"encoding/json"

	"git.subcult.tv/subculture-collective/mantik/internal/externalplan"
	"git.subcult.tv/subculture-collective/mantik/internal/workerplan"
)

// execution is private, constructed only from a validated supported declaration.
// Jobs cannot supply commands, environment values, mounts or result validators.
type execution struct {
	behaviorTmp                                 bool
	raw                                         []byte
	sha, image                                  string
	resources                                   workerplan.Resources
	acquisitionSchema, receiptSchema, authority string
	entrypoint, args, env                       []string
	outcome                                     func([]byte) (outcome, error)
}
type outcome struct {
	sha, workerStatus, status, reason string
	exit                              int
	eligible                          bool
}

func nativeExecution(j workerplan.Job) execution {
	raw, _ := json.Marshal(j)
	return execution{raw: raw, sha: j.SHA256, image: j.ImageSHA256, resources: j.Resources,
		acquisitionSchema: "mantik-worker-acquisition-v1", receiptSchema: "mantik-worker-host-receipt-v1", authority: "synthetic_host_launch_receipt_only",
		entrypoint: []string{"/app/mantik"}, args: workerArgs(j), env: []string{"PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin"},
		outcome: func(raw []byte) (outcome, error) {
			r, err := workerplan.ReadResult(bytes.NewReader(raw), j)
			if err != nil {
				return outcome{}, err
			}
			o := outcome{sha: r.SHA256, workerStatus: r.Status, status: r.Status, reason: r.ErrorCode, exit: 1, eligible: r.DeadlineMet}
			if r.Status == "completed" {
				o.reason = "validated_worker_result"
				o.exit = 0
			}
			return o, nil
		}}
}
func externalExecution(j externalplan.Job) execution {
	raw, _ := json.Marshal(j)
	args := []string{"-I", "/adapter/adapter.py"}
	for _, a := range j.Inputs {
		name := a.Reference[:len(a.Reference)-len(".json")]
		args = append(args, "--"+name, "/worker/"+a.Reference, "--"+name+"-sha256", a.SHA256)
	}
	// These public image constants were inspected on the digest-pinned Python
	// build. No caller environment is forwarded; any added variable is rejected.
	env := []string{"PATH=/usr/local/bin:/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin", "GPG_KEY=7169605F62C751356D054A26A821E680E5FA6305", "PYTHON_VERSION=3.13.9", "PYTHON_SHA256=ed5ef34cda36cfa2f3a340f07cac7e7814f91c7f3c411f6d3562323a866c5c66", "PYTHONDONTWRITEBYTECODE=1", "PYTHONUNBUFFERED=1", "OPENBLAS_NUM_THREADS=1", "OMP_NUM_THREADS=1"}
	return execution{raw: raw, sha: j.SHA256, image: j.ImageSHA256, resources: j.Resources,
		acquisitionSchema: "mantik-external-worker-acquisition-v1", receiptSchema: "mantik-external-worker-host-receipt-v1", authority: "synthetic_external_host_launch_receipt_only",
		entrypoint: []string{"/usr/local/bin/python"}, args: args, env: env,
		outcome: func(raw []byte) (outcome, error) {
			r, err := externalplan.ReadResult(bytes.NewReader(raw), j)
			if err != nil {
				return outcome{}, err
			}
			o := outcome{sha: r.SHA256, workerStatus: r.Status, status: "failed", reason: r.ErrorCode, exit: 1, eligible: true}
			if r.Status == "compared" {
				o.status = "mismatch"
				o.reason = "comparison_metrics_differ"
				if *r.AllMatch {
					o.status = "completed"
					o.reason = "validated_external_comparison"
					o.exit = 0
				}
			}
			return o, nil
		}}
}
