package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"git.subcult.tv/subculture-collective/mantik/internal/experiment"
	"git.subcult.tv/subculture-collective/mantik/internal/replay"
	"git.subcult.tv/subculture-collective/mantik/internal/workerplan"
)

func TestOfflineWorkerCLI(t *testing.T) {
	t.Setenv("MANTIK_API_TOKEN", "worker-test-secret-canary")
	t.Setenv("DATABASE_URL", "not-a-service-database")
	ctx := context.Background()
	root := t.TempDir()
	d, _ := replay.FixtureDataset()
	recipe, _ := experiment.DefaultRecipe(d)
	writeFile := func(name string, value any) string {
		t.Helper()
		p := filepath.Join(root, name)
		raw, _ := json.MarshalIndent(value, "", "  ")
		if err := os.WriteFile(p, raw, 0600); err != nil {
			t.Fatal(err)
		}
		return p
	}
	input := writeFile("dataset.json", d)
	recipeFile := writeFile("recipe.json", recipe)
	run := func(args ...string) []byte {
		t.Helper()
		var out, errOut bytes.Buffer
		if err := Run(ctx, args, &out, &errOut); err != nil {
			t.Fatal(args, err, errOut.String())
		}
		if strings.Contains(out.String(), "worker-test-secret-canary") {
			t.Fatal("host secret entered protocol")
		}
		return out.Bytes()
	}
	var j workerplan.Job
	if err := json.Unmarshal(run("worker-job", "--id", "offline-job", "--image-sha256", "sha256:"+strings.Repeat("a", 64), "--recipe", recipeFile, "--recipe-sha256", recipe.SHA256, "--input", input), &j); err != nil || j.Validate() != nil {
		t.Fatal(j, err)
	}
	jobFile := writeFile("job.json", j)
	args := []string{"worker-execute", "--job", jobFile, "--job-sha256", j.SHA256, "--input", input}
	first := run(args...)
	second := run(args...)
	if !bytes.Equal(first, second) {
		t.Fatal("successful protocol retry changed result")
	}
	r, err := workerplan.ReadResult(bytes.NewReader(first), j)
	if err != nil || r.Status != "completed" || r.IsolationVerified || r.ImageIdentityVerified {
		t.Fatal(r, err)
	}
	resultFile := writeFile("result.json", r)
	if got := run("worker-result", "--job", jobFile, "--job-sha256", j.SHA256, "--file", resultFile); !bytes.Equal(got, first) {
		t.Fatal("result inspection changed envelope")
	}
	for _, bad := range [][]string{
		{"worker-job"}, {"worker-execute", "--job", jobFile, "--job-sha256", strings.Repeat("0", 64), "--input", input},
		{"worker-execute", "--job", jobFile, "--job-sha256", j.SHA256, "--input", recipeFile},
		{"worker-execute", "--job", jobFile, "--job-sha256", j.SHA256, "--input", input, "--api-url", "http://127.0.0.1:41890"},
	} {
		var out, errOut bytes.Buffer
		if err := Run(ctx, bad, &out, &errOut); err == nil || out.Len() != 0 {
			t.Fatal("invalid request executed or printed a result", bad, err)
		}
	}
	changed := j
	changed.Recipe.DatasetID = "missing"
	changed.Recipe, _ = experiment.Seal(changed.Recipe)
	changed, _ = workerplan.Seal(changed)
	failedJob := writeFile("failed-job.json", changed)
	var out, errOut bytes.Buffer
	if err := Run(ctx, []string{"worker-execute", "--job", failedJob, "--job-sha256", changed.SHA256, "--input", input}, &out, &errOut); err == nil {
		t.Fatal("failed native run exited successfully")
	}
	failed, err := workerplan.ReadResult(&out, changed)
	if err != nil || failed.ErrorCode != "dataset_pin_mismatch" {
		t.Fatal("failure envelope lost", failed, err)
	}
}
