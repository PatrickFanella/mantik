package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"git.subcult.tv/subculture-collective/mantik/internal/externalplan"
)

func TestExternalWorkerCLIIsOfflineAndRejectsChangedArtifacts(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	t.Setenv("DATABASE_URL", "not-a-database")
	t.Setenv("MANTIK_API_TOKEN", "host-canary-not-forwarded")
	run := func(args ...string) ([]byte, error) {
		var out, errOut bytes.Buffer
		err := Run(ctx, args, &out, &errOut)
		return out.Bytes(), err
	}
	for _, item := range []struct {
		name string
		args []string
	}{
		{"dataset.json", []string{"replay", "--export-dataset"}},
		{"recipe.json", []string{"experiment", "--export-recipe"}},
		{"native.json", []string{"experiment", "--attempt", "external-cli-reference", "--out-dir", filepath.Join(root, "native-reference")}},
	} {
		raw, err := run(item.args...)
		if err != nil {
			t.Fatal(err)
		}
		if err = os.WriteFile(filepath.Join(root, item.name), raw, 0600); err != nil {
			t.Fatal(err)
		}
	}
	args := []string{"external-worker-job", "--id", "external-cli", "--image-sha256", "sha256:" + strings.Repeat("a", 64), "--adapter-sha256", strings.Repeat("b", 64), "--requirements-sha256", strings.Repeat("c", 64), "--input-dir", root}
	raw, err := run(args...)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(raw, []byte("host-canary")) {
		t.Fatal("host environment leaked")
	}
	j, err := externalplan.ReadJob(bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	job := filepath.Join(root, "job.json")
	if err = os.WriteFile(job, raw, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = run("worker-result", "--job", job, "--job-sha256", j.SHA256, "--file", job); err == nil {
		t.Fatal("native command accepted external job")
	}
	if _, err = run("external-worker-launch", "--job", job, "--job-sha256", strings.Repeat("f", 64), "--input-dir", root, "--out-dir", filepath.Join(root, "never-created"), "--approved-image-sha256", j.ImageSHA256); err == nil {
		t.Fatal("changed job pin accepted")
	}
	if _, err = os.Stat(filepath.Join(root, "never-created")); !os.IsNotExist(err) {
		t.Fatal("invalid job acquired resources")
	}
	if _, err = run("external-worker-result", "--job", job, "--job-sha256", j.SHA256, "--file", job); err == nil {
		t.Fatal("malformed external result accepted")
	}
	if _, err = run(append(args, "--api-url", "http://forbidden.invalid")...); err == nil {
		t.Fatal("provider/service flag accepted")
	}
	var declaration map[string]any
	_ = json.Unmarshal(raw, &declaration)
	if declaration["schema"] != externalplan.JobSchema {
		t.Fatal("wrong schema")
	}
	if err = os.WriteFile(filepath.Join(root, "native.json"), []byte(`{"qualified":true}`), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = run(args...); err == nil {
		t.Fatal("invalid native reference accepted")
	}
}
