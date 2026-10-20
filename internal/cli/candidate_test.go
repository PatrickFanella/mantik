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
	"git.subcult.tv/subculture-collective/mantik/internal/hypothesis"
	"git.subcult.tv/subculture-collective/mantik/internal/replay"
)

func TestCandidateCLIIsOfflineAndPreservesSource(t *testing.T) {
	t.Setenv("MANTIK_API_TOKEN", "must-not-be-used")
	t.Setenv("DATABASE_URL", "must-not-be-used")
	dir := t.TempDir()
	marker := filepath.Join(dir, "executed")
	source := "package main\nimport \"os\"\nfunc init(){ os.WriteFile(" + `"` + marker + `"` + ",[]byte(\"unexpected\"),0600) }\n"
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
	h, _ := json.Marshal(hypothesis.Fixture())
	declaration := write("hypothesis.json", h)
	src := write("candidate.go", []byte(source))
	args := []string{"candidate-seal", "--source", src, "--hypothesis", declaration, "--dataset", dataset, "--dataset-sha256", d.SHA256, "--toolchain-image-sha256", "sha256:" + strings.Repeat("a", 64)}
	var out, errs bytes.Buffer
	if err := Run(context.Background(), args, &out, &errs); err != nil {
		t.Fatal(err, errs.String())
	}
	var p candidate.Package
	if json.Unmarshal(out.Bytes(), &p) != nil || p.Validate() != nil || p.Source.Text != source {
		t.Fatal("invalid readback")
	}
	file := write("sealed.json", out.Bytes())
	out.Reset()
	inspect := []string{"candidate-inspect", "--file", file, "--sha256", p.SHA256, "--dataset", dataset}
	if err := Run(context.Background(), inspect, &out, &errs); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("candidate source was executed")
	}
	if strings.Contains(out.String(), "must-not-be-used") {
		t.Fatal("ambient credentials copied")
	}
	if err := os.WriteFile(dataset, append(raw, '\n'), 0600); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	if err := Run(context.Background(), inspect, &out, &errs); err == nil || out.Len() != 0 {
		t.Fatal("changed raw dataset accepted")
	}
	for _, args := range [][]string{{"candidate-seal"}, {"candidate-inspect", "--file", file}, {"candidate-seal", "--api-url", "http://127.0.0.1:1"}, {"candidate-inspect", "--file", file, "--sha256", strings.Repeat("0", 64)}} {
		out.Reset()
		if err := Run(context.Background(), args, &out, &errs); err == nil || out.Len() != 0 {
			t.Fatal("invalid flags/identity accepted", args)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := Run(ctx, inspect, &out, &errs); err != context.Canceled {
		t.Fatal(err)
	}
}
