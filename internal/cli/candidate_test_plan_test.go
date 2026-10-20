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
	"git.subcult.tv/subculture-collective/mantik/internal/candidatetest"
	"git.subcult.tv/subculture-collective/mantik/internal/hypothesis"
	"git.subcult.tv/subculture-collective/mantik/internal/replay"
)

func TestCandidateFixturePlanCLIUsesRecordingsOnly(t *testing.T) {
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
	record := candidatetest.Recording{Schema: candidatetest.RecordingSchema, AttemptID: "fixture-recording", PlanSHA256: plan.SHA256, CandidateSHA256: p.SHA256, Status: "unknown", ReasonCode: "worker_lost", Outputs: []candidatetest.Output{}}
	raw, _ = json.Marshal(record)
	recordFile := write("recording.json", raw)
	evaluate := []string{"candidate-test-evaluate", "--plan", planFile, "--plan-sha256", plan.SHA256, "--dataset", dataset, "--recording", recordFile}
	out.Reset()
	if err := Run(context.Background(), evaluate, &out, &errs); err != nil {
		t.Fatal(err)
	}
	var report candidatetest.Report
	if json.Unmarshal(out.Bytes(), &report) != nil || report.Status != "incomplete" || report.Missing != 4 || report.ExecutionVerified || report.AllAnnotationsMatch {
		t.Fatal(out.String())
	}
	if strings.Contains(out.String(), "must-not-") {
		t.Fatal("ambient credentials copied")
	}
	for _, args := range [][]string{{"candidate-test-plan"}, {"candidate-test-evaluate"}, {"candidate-test-plan", "--api-url", "http://127.0.0.1:1"}, {"candidate-test-evaluate", "--plan", planFile, "--plan-sha256", strings.Repeat("0", 64), "--dataset", dataset, "--recording", recordFile}} {
		out.Reset()
		if err := Run(context.Background(), args, &out, &errs); err == nil || out.Len() != 0 {
			t.Fatal("missing/bad pins accepted", args)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := Run(ctx, evaluate, &out, &errs); err != context.Canceled {
		t.Fatal(err)
	}
}
