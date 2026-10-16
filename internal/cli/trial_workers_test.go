package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
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
	testReportedAttempts(t, ctx, base, receiver, first, run)
	if err = j.ValidateInputs(inputs); err != nil {
		t.Fatal("service bundle cannot be consumed by durable external launcher", err)
	}
}

func testReportedAttempts(t *testing.T, ctx context.Context, base, receiver string, job workertrial.Record, run func(...string) []byte) {
	t.Helper()
	var i workertrial.Intent
	args := []string{"trial-worker-attempt-start", "--id", "physical-1", "--receiver-id", receiver, "--job", job.Command.RequestID, "--declaration-sha256", job.SHA256}
	if err := json.Unmarshal(run(args...), &i); err != nil || i.Validate() != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	stdout, stderr := filepath.Join(dir, "stdout.json"), filepath.Join(dir, "stderr.txt")
	for path, body := range map[string]string{stdout: "{partial output\n", stderr: "fixture error\n"} {
		if err := os.WriteFile(path, []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
	}
	reportArgs := []string{"trial-worker-attempt-report", "--id", "physical-1", "--receiver-id", receiver, "--intent-sha256", i.SHA256, "--status", "incomplete", "--stdout-file", stdout, "--stderr-file", stderr}
	var o, again workertrial.Outcome
	if err := json.Unmarshal(run(reportArgs...), &o); err != nil || o.Validate(i) != nil || o.ComparisonValid {
		t.Fatal(o, err)
	}
	if err := json.Unmarshal(run(reportArgs...), &again); err != nil || again.SHA256 != o.SHA256 {
		t.Fatal(err)
	}
	var a workertrial.Attempt
	if err := json.Unmarshal(run("trial-worker-attempt", "--id", "physical-1"), &a); err != nil || a.Validate() != nil || a.Outcome.SHA256 != o.SHA256 {
		t.Fatal(err)
	}
	var all []workertrial.Attempt
	if err := json.Unmarshal(run("trial-worker-attempts", "--id", job.Command.RequestID), &all); err != nil || len(all) != 1 {
		t.Fatal(all, err)
	}
	for name, path := range map[string]string{"stdout": stdout, "stderr": stderr} {
		want, _ := os.ReadFile(path)
		got := run("trial-worker-attempt-output", "--id", "physical-1", "--name", name, "--raw")
		if !bytes.Equal(got, want) {
			t.Fatal("exact output changed")
		}
		var wrapped workertrial.Artifact
		if err := json.Unmarshal(run("trial-worker-attempt-output", "--id", "physical-1", "--name", name), &wrapped); err != nil || wrapped.Content != string(want) {
			t.Fatal(err)
		}
	}
	call := func(method, path, body string, auth bool, want int) {
		t.Helper()
		req, _ := http.NewRequestWithContext(ctx, method, base+path, strings.NewReader(body))
		if auth {
			req.Header.Set("Authorization", "Bearer trial-cli-token")
		}
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		if res.StatusCode != want {
			t.Fatal(path, res.StatusCode, want)
		}
	}
	body, _ := json.Marshal(i.Command)
	report, _ := json.Marshal(workertrial.ReportCommand{ReceiverID: receiver, AttemptID: "physical-1", IntentSHA256: i.SHA256, ReportedStatus: "incomplete", Stdout: "{partial output\n", Stderr: "fixture error\n"})
	for _, path := range []string{"/v1/external-trial-attempts/physical-1", "/v1/external-trial-attempts/physical-1/outputs/stdout", "/v1/external-trial-jobs/external/attempts"} {
		call("GET", path, "", false, 401)
	}
	call("POST", "/v1/external-trial-attempts", string(body), false, 401)
	call("POST", "/v1/external-trial-attempts/physical-1/report", string(report), false, 401)
	call("POST", "/v1/external-trial-attempts", strings.Replace(string(body), `"job_id":`, `"host_state_known":true,"job_id":`, 1), true, 422)
	call("POST", "/v1/external-trial-attempts", strings.Replace(string(body), `"job_id":`, `"job_id":"x","job_id":`, 1), true, 422)
	call("POST", "/v1/external-trial-attempts/physical-1/report", strings.Replace(string(report), `"stdout":`, `"qualification_credit":true,"stdout":`, 1), true, 422)
	call("POST", "/v1/external-trial-attempts/physical-1/report", strings.Replace(string(report), `"stdout":`, `"stdout":"x","stdout":`, 1), true, 422)
	call("POST", "/v1/external-trial-attempts/other/report", string(report), true, 422)
	call("GET", "/v1/external-trial-jobs/external/attempts?best=true", "", true, 422)
	call("GET", "/v1/external-trial-attempts/physical-1?launch=true", "", true, 422)
	call("GET", "/v1/external-trial-attempts/missing", "", true, 404)
	call("GET", "/v1/external-trial-attempts/physical-1/outputs/other", "", true, 422)
	call("POST", "/v1/external-trial-attempts/physical-1/launch", `{}`, true, 404)
}
