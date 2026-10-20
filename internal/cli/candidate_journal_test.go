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

func TestCandidateJournalCLIRejectsBeforeIntent(t *testing.T) {
	dir := t.TempDir()
	d, _ := replay.FixtureDataset()
	raw, _ := json.Marshal(d)
	dataset := filepath.Join(dir, "dataset.json")
	if err := os.WriteFile(dataset, raw, 0600); err != nil {
		t.Fatal(err)
	}
	c, err := candidate.New([]byte("package candidate\nfunc Entry(int64)bool{return true}"), hypothesis.Fixture(), raw, "sha256:"+strings.Repeat("a", 64))
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
	job := filepath.Join(dir, "job.json")
	raw, _ = json.Marshal(j)
	if err := os.WriteFile(job, raw, 0600); err != nil {
		t.Fatal(err)
	}
	journal := filepath.Join(dir, "must-not-exist")
	base := []string{"candidate-attempt-run", "--journal", journal, "--request-id", "one", "--job", job, "--job-sha256", j.SHA256, "--dataset", dataset, "--approved-image-sha256", "sha256:" + strings.Repeat("b", 64)}
	for _, args := range [][]string{base, {"candidate-attempt-run"}, {"candidate-attempt-reconcile"}, {"candidate-attempt-history"}, {"candidate-attempt-history", "--journal", journal}, {"candidate-attempt-run", "--api-url", "http://127.0.0.1:1"}} {
		var out, errs bytes.Buffer
		if err := Run(context.Background(), args, &out, &errs); err == nil || out.Len() != 0 {
			t.Fatal("invalid request accepted", args)
		}
	}
	if _, err := os.Stat(journal); !os.IsNotExist(err) {
		t.Fatal("journal created before pins verified")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	var out, errs bytes.Buffer
	if err := Run(ctx, base, &out, &errs); err != context.Canceled {
		t.Fatal(err)
	}
}
