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
	"git.subcult.tv/subculture-collective/mantik/internal/candidatebuild"
	"git.subcult.tv/subculture-collective/mantik/internal/hypothesis"
	"git.subcult.tv/subculture-collective/mantik/internal/replay"
)

func TestCandidateBuildDeclarationCLI(t *testing.T) {
	d, _ := replay.FixtureDataset()
	raw, _ := json.Marshal(d)
	p, err := candidate.New([]byte("package candidate\nfunc Entry(int64)bool{return true}"), hypothesis.Fixture(), raw, "sha256:"+strings.Repeat("a", 64))
	if err != nil {
		t.Fatal(err)
	}
	raw, _ = json.Marshal(p)
	file := filepath.Join(t.TempDir(), "candidate.json")
	if err := os.WriteFile(file, raw, 0600); err != nil {
		t.Fatal(err)
	}
	args := []string{"candidate-build-job", "--candidate", file, "--candidate-sha256", p.SHA256}
	var out, errs bytes.Buffer
	if err := Run(context.Background(), args, &out, &errs); err != nil {
		t.Fatal(err)
	}
	j, err := candidatebuild.ReadJob(bytes.NewReader(out.Bytes()))
	if err != nil || j.Candidate.ID != p.ID {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"candidate-build-job"}, {"candidate-build-job", "--candidate", file, "--candidate-sha256", strings.Repeat("0", 64)}, {"candidate-build-job", "--compiler", "go"}, {"candidate-build-launch"}, {"candidate-build-reconcile"}, {"candidate-build-execute"}, {"candidate-build-result"}} {
		out.Reset()
		if err := Run(context.Background(), args, &out, &errs); err == nil || out.Len() != 0 {
			t.Fatal("missing pins/unsupported arguments accepted", args)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := Run(ctx, args, &out, &errs); err != context.Canceled {
		t.Fatal(err)
	}
}
