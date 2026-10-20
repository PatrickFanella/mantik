package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"git.subcult.tv/subculture-collective/mantik/internal/candidate"
	"git.subcult.tv/subculture-collective/mantik/internal/candidateexec"
	"git.subcult.tv/subculture-collective/mantik/internal/candidateproperty"
	"git.subcult.tv/subculture-collective/mantik/internal/candidatetest"
	"git.subcult.tv/subculture-collective/mantik/internal/hypothesis"
	"git.subcult.tv/subculture-collective/mantik/internal/replay"
	"git.subcult.tv/subculture-collective/mantik/internal/workerplan"
)

func TestCandidatePropertyCLIIsOfflineAndRequiresPins(t *testing.T) {
	t.Setenv("DATABASE_URL", "do-not-connect")
	t.Setenv("MANTIK_API_TOKEN", "do-not-forward")
	dir := t.TempDir()
	write := func(name string, raw []byte) string {
		t.Helper()
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, raw, 0600); err != nil {
			t.Fatal(err)
		}
		return path
	}
	data, _ := replay.FixtureDataset()
	raw, _ := json.Marshal(data)
	dataset := write("dataset.json", raw)
	c, err := candidate.New([]byte("package candidate\nfunc Entry(int64)bool{panic(1)}"), hypothesis.Fixture(), raw, "sha256:"+strings.Repeat("a", 64))
	if err != nil {
		t.Fatal(err)
	}
	p, err := candidatetest.New(c, raw, []bool{true, true, true, true})
	if err != nil {
		t.Fatal(err)
	}
	j, err := candidateexec.New(p, raw)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ = json.Marshal(j)
	job := write("job.json", raw)
	properties := write("properties.json", []byte(`["same_price_same_signal"]`))
	args := []string{"candidate-property-plan", "--job", job, "--job-sha256", j.SHA256, "--dataset", dataset, "--properties", properties}
	var out, errs bytes.Buffer
	if err := Run(context.Background(), args, &out, &errs); err != nil {
		t.Fatal(err)
	}
	plan, err := candidateproperty.ReadPlan(bytes.NewReader(out.Bytes()))
	if err != nil {
		t.Fatal(err)
	}
	planFile := write("property-plan.json", out.Bytes())
	r := candidateexec.Result{Schema: candidateexec.ResultSchema, JobSHA256: j.SHA256, PlanSHA256: j.Plan.SHA256, CandidateSHA256: c.SHA256, Status: "completed", ReasonCode: "fixture_compared", BuildCompleted: true, RunAttempted: true, Values: []bool{true, true, true, true}, Matched: 4, AllAnnotationsMatch: true, Authority: "synthetic_candidate_behavior_worker_only"}
	for i := range r.Values {
		r.RunStderr += fmt.Sprintf("_mantik %d true\n", i)
	}
	r.SHA256 = workerplan.Hash(r)
	raw, _ = json.Marshal(r)
	result := write("result.json", raw)
	evaluate := []string{"candidate-property-evaluate", "--plan", planFile, "--plan-sha256", plan.SHA256, "--dataset", dataset, "--file", result}
	out.Reset()
	if err := Run(context.Background(), evaluate, &out, &errs); err != nil {
		t.Fatal(err)
	}
	var report candidateproperty.Report
	if json.Unmarshal(out.Bytes(), &report) != nil || report.Status != "incomplete" || report.Checks[0].Status != "not_applicable" || report.AllSamplePropertiesSatisfied || report.ExecutionVerified || report.QualificationCredit {
		t.Fatal(out.String())
	}
	if strings.Contains(out.String(), "do-not-") {
		t.Fatal("ambient credentials copied")
	}
	for _, bad := range [][]string{{"candidate-property-plan"}, {"candidate-property-evaluate"}, {"candidate-property-plan", "--api-url", "http://127.0.0.1:1"}, {"candidate-property-evaluate", "--plan", planFile, "--plan-sha256", strings.Repeat("0", 64), "--dataset", dataset, "--file", result}} {
		out.Reset()
		if err := Run(context.Background(), bad, &out, &errs); err == nil || out.Len() != 0 {
			t.Fatal("invalid request accepted", bad)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := Run(ctx, evaluate, &out, &errs); err != context.Canceled {
		t.Fatal(err)
	}
}
