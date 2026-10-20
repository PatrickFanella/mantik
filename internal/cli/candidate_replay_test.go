package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/candidate"
	"git.subcult.tv/subculture-collective/mantik/internal/candidateexec"
	"git.subcult.tv/subculture-collective/mantik/internal/candidatereplay"
	"git.subcult.tv/subculture-collective/mantik/internal/candidatetest"
	"git.subcult.tv/subculture-collective/mantik/internal/hypothesis"
	"git.subcult.tv/subculture-collective/mantik/internal/replay"
)

func TestCandidateReplayCLIExportsExactVerifiedBytes(t *testing.T) {
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
	data, _ := json.Marshal(d)
	dataset := write("dataset.json", data)
	c, err := candidate.New([]byte("package candidate\nfunc Entry(int64)bool{panic(1)}"), hypothesis.Fixture(), data, "sha256:"+strings.Repeat("a", 64))
	if err != nil {
		t.Fatal(err)
	}
	parent, err := candidatetest.New(c, data, []bool{true, true, true, true})
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(parent)
	parentFile := write("parent.json", raw)
	at := d.Records[len(d.Records)-1].VisibleAt().Add(time.Hour).Format(time.RFC3339Nano)
	args := []string{"candidate-replay-plan", "--plan", parentFile, "--plan-sha256", parent.SHA256, "--dataset", dataset, "--cutoff", at}
	var out, errs bytes.Buffer
	if err := Run(context.Background(), args, &out, &errs); err != nil {
		t.Fatal(err)
	}
	p, err := candidatereplay.Read(bytes.NewReader(out.Bytes()))
	if err != nil || p.Job == nil {
		t.Fatal(err)
	}
	planFile := write("replay.json", out.Bytes())
	export := []string{"candidate-replay-input", "--plan", planFile, "--plan-sha256", p.SHA256, "--dataset", dataset, "--artifact", "dataset"}
	out.Reset()
	if err := Run(context.Background(), export, &out, &errs); err != nil {
		t.Fatal(err)
	}
	if p.Job.Plan.VerifyDataset(out.Bytes()) != nil || bytes.HasSuffix(out.Bytes(), []byte("\n")) {
		t.Fatal("dataset export changed pinned bytes")
	}
	export[len(export)-1] = "job"
	out.Reset()
	if err := Run(context.Background(), export, &out, &errs); err != nil {
		t.Fatal(err)
	}
	j, err := candidateexec.ReadJob(bytes.NewReader(out.Bytes()))
	if err != nil || j.SHA256 != p.Job.SHA256 {
		t.Fatal(err)
	}
	for _, bad := range [][]string{{"candidate-replay-plan"}, {"candidate-replay-input"}, {"candidate-replay-plan", "--plan", parentFile, "--plan-sha256", parent.SHA256, "--dataset", dataset}, {"candidate-replay-input", "--plan", planFile, "--plan-sha256", strings.Repeat("0", 64), "--dataset", dataset, "--artifact", "job"}} {
		out.Reset()
		if err := Run(context.Background(), bad, &out, &errs); err == nil || out.Len() != 0 {
			t.Fatal("bad replay request accepted", bad)
		}
	}
	early, err := candidatereplay.New(parent, data, d.Records[0].VisibleAt().Add(-24*time.Hour))
	if err != nil || early.Job != nil {
		t.Fatal(err)
	}
	raw, _ = json.Marshal(early)
	earlyFile := write("early.json", raw)
	out.Reset()
	if err := Run(context.Background(), []string{"candidate-replay-input", "--plan", earlyFile, "--plan-sha256", early.SHA256, "--dataset", dataset, "--artifact", "job"}, &out, &errs); err != candidatereplay.ErrNoInputs || out.Len() != 0 {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := Run(ctx, args, &out, &errs); err != context.Canceled {
		t.Fatal(err)
	}
}
