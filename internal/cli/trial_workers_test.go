package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	"git.subcult.tv/subculture-collective/mantik/internal/externalplan"
	"git.subcult.tv/subculture-collective/mantik/internal/trial"
	"git.subcult.tv/subculture-collective/mantik/internal/workertrial"
)

func testTrialWorkerHandoff(t *testing.T, ctx context.Context, base, receiver string, native trial.Run) {
	t.Helper()
	run := func(args ...string) []byte {
		t.Helper()
		var out, errOut bytes.Buffer
		args = append(args, "--api-url", base)
		if err := Run(ctx, args, &out, &errOut); err != nil {
			t.Fatal(args, err, errOut.String())
		}
		return out.Bytes()
	}
	args := []string{"trial-worker-declare", "--id", "external", "--receiver-id", receiver, "--attempt", native.AttemptID, "--native-run-sha256", native.SHA256, "--image-sha256", "sha256:" + strings.Repeat("a", 64), "--adapter-sha256", strings.Repeat("b", 64), "--requirements-sha256", strings.Repeat("c", 64)}
	var first, retry, actual workertrial.Record
	if err := json.Unmarshal(run(args...), &first); err != nil || first.Validate() != nil || first.ExecutionStarted || first.HostStateKnown || first.Job.Inputs[2].SHA256 != native.ResultSHA256 {
		t.Fatal(first, err)
	}
	if err := json.Unmarshal(run(args...), &retry); err != nil || retry.SHA256 != first.SHA256 {
		t.Fatal("declaration retry changed", err)
	}
	if err := json.Unmarshal(run("trial-worker", "--id", "external"), &actual); err != nil || actual.SHA256 != first.SHA256 {
		t.Fatal(err)
	}
	j, err := externalplan.ReadJob(bytes.NewReader(run("trial-worker", "--id", "external", "--job-only")))
	if err != nil || j.SHA256 != first.Job.SHA256 {
		t.Fatal(err)
	}
	inputs := map[string][]byte{}
	for _, a := range j.Inputs {
		inputs[a.Reference] = run("trial-worker-input", "--id", "external", "--name", a.Reference, "--raw")
		var wrapped workertrial.Artifact
		if err = json.Unmarshal(run("trial-worker-input", "--id", "external", "--name", a.Reference), &wrapped); err != nil || wrapped.Content != string(inputs[a.Reference]) || wrapped.SHA256 != a.SHA256 {
			t.Fatal(err)
		}
	}
	if err = j.ValidateInputs(inputs); err != nil {
		t.Fatal("service bundle cannot be consumed by durable external launcher", err)
	}
}
