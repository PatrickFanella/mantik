package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"git.subcult.tv/subculture-collective/mantik/internal/candidate"
	"git.subcult.tv/subculture-collective/mantik/internal/candidateexec"
	"git.subcult.tv/subculture-collective/mantik/internal/candidatetest"
	"git.subcult.tv/subculture-collective/mantik/internal/hypothesis"
	"git.subcult.tv/subculture-collective/mantik/internal/replay"
)

func TestCandidateBehaviorJobCLIHasExplicitPins(t *testing.T) {
	t.Setenv("DATABASE_URL", "must-not-connect")
	t.Setenv("MANTIK_API_TOKEN", "must-not-forward")
	dir := t.TempDir()
	write := func(name string, raw []byte) string {
		t.Helper()
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, raw, 0600); err != nil {
			t.Fatal(err)
		}
		return p
	}
	d, _ := replay.FixtureDataset()
	raw, _ := json.Marshal(d)
	dataset := write("dataset.json", raw)
	p, err := candidate.New([]byte("package candidate\nfunc Entry(int64)bool{panic(\"never run\")}"), hypothesis.Fixture(), raw, "sha256:"+strings.Repeat("a", 64))
	if err != nil {
		t.Fatal(err)
	}
	raw, _ = json.Marshal(p)
	source := write("candidate.json", raw)
	annotations := write("expectations.json", []byte("[false,true,false,true]"))
	args := []string{"candidate-test-plan", "--candidate", source, "--candidate-sha256", p.SHA256, "--dataset", dataset, "--expectations", annotations}
	var out, errs bytes.Buffer
	if err := Run(context.Background(), args, &out, &errs); err != nil {
		t.Fatal(err)
	}
	plan, err := candidatetest.ReadPlan(bytes.NewReader(out.Bytes()))
	if err != nil {
		t.Fatal(err)
	}
	planFile := write("plan.json", out.Bytes())

	args = []string{"candidate-behavior-job", "--plan", planFile, "--plan-sha256", plan.SHA256, "--dataset", dataset}
	out.Reset()
	if err := Run(context.Background(), args, &out, &errs); err != nil {
		t.Fatal(err)
	}
	job, err := candidateexec.ReadJob(bytes.NewReader(out.Bytes()))
	if err != nil || job.Plan.SHA256 != plan.SHA256 || job.QualificationCredit {
		t.Fatal(err)
	}
	jobFile := write("job.json", out.Bytes())
	if strings.Contains(out.String(), "must-not-") {
		t.Fatal("ambient credential copied")
	}
	for _, bad := range [][]string{{"candidate-behavior-job"}, {"candidate-behavior-launch"}, {"candidate-behavior-reconcile"}, {"candidate-behavior-result"}, {"candidate-behavior-job", "--api-url", "http://127.0.0.1:1"}, {"candidate-behavior-result", "--job", jobFile, "--job-sha256", strings.Repeat("0", 64), "--file", jobFile}, {"candidate-behavior-launch", "--job", jobFile, "--job-sha256", job.SHA256, "--out-dir", filepath.Join(dir, "forbidden"), "--approved-image-sha256", job.Plan.Candidate.Toolchain.ImageSHA256}} {
		out.Reset()
		if err := Run(context.Background(), bad, &out, &errs); err == nil || out.Len() != 0 {
			t.Fatal("missing/pinless inputs accepted", bad)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "forbidden")); !os.IsNotExist(err) {
		t.Fatal("attempt created without dataset")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := Run(ctx, args, &out, &errs); err != context.Canceled {
		t.Fatal(err)
	}
}
