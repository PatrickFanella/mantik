package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"git.subcult.tv/subculture-collective/mantik/internal/domain"
	"git.subcult.tv/subculture-collective/mantik/internal/experiment"
	"git.subcult.tv/subculture-collective/mantik/internal/replay"
	"git.subcult.tv/subculture-collective/mantik/internal/service"
	"git.subcult.tv/subculture-collective/mantik/internal/storage"
)

func TestDemoLocalAndRemote(t *testing.T) {
	server := httptest.NewServer(service.Handler(storage.NewMemory(), ""))
	defer server.Close()
	for _, args := range [][]string{{"demo"}, {"demo", "--api-url", server.URL}, {"demo", "--api-url", server.URL}} {
		var out, errOut bytes.Buffer
		if err := Run(context.Background(), args, &out, &errOut); err != nil {
			t.Fatal(err)
		}
		var snapshot domain.Snapshot
		if err := json.Unmarshal(out.Bytes(), &snapshot); err != nil {
			t.Fatal(err)
		}
		if snapshot.State.Case.Status != "closed" || snapshot.ObservationCount != 4 {
			t.Fatal("CLI lifecycle incomplete")
		}
	}
}

func TestReplayExportAndFile(t *testing.T) {
	var exported, errOut bytes.Buffer
	if err := Run(context.Background(), []string{"replay", "--export-dataset"}, &exported, &errOut); err != nil {
		t.Fatal(err)
	}
	dataset, err := replay.Read(bytes.NewReader(exported.Bytes()))
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "dataset.json")
	if err := os.WriteFile(path, exported.Bytes(), 0600); err != nil {
		t.Fatal(err)
	}
	var local, file bytes.Buffer
	args := []string{"replay", "--cutoffs", "2026-01-05T15:01:00Z,2026-01-05T15:03:00Z"}
	if err := Run(context.Background(), args, &local, &errOut); err != nil {
		t.Fatal(err)
	}
	if err := Run(context.Background(), append(args, "--dataset", path, "--dataset-sha256", dataset.SHA256), &file, &errOut); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(local.Bytes(), file.Bytes()) {
		t.Fatal("file replay differs from in-memory dataset")
	}
	var result replay.Result
	if err := json.Unmarshal(file.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if len(result.Frames) != 2 || result.Frames[1].Snapshot.State.Case.Exit.RealizedPnLCents != -6000 {
		t.Fatal("CLI replay incomplete")
	}
	for _, args := range [][]string{{"replay"}, {"replay", "--cutoffs", "now"}, {"replay", "--dataset", path}, {"replay", "--dataset", path, "--dataset-sha256", "wrong", "--cutoffs", "2026-01-05T15:03:00Z"}, {"replay", "--export-dataset", "--cutoffs", "2026-01-05T15:03:00Z"}} {
		if err := Run(context.Background(), args, &bytes.Buffer{}, &errOut); err == nil {
			t.Fatal("invalid replay request accepted")
		}
	}
}

func TestExperimentRetentionAndPinnedRecipe(t *testing.T) {
	var exported, errOut bytes.Buffer
	if err := Run(context.Background(), []string{"experiment", "--export-recipe"}, &exported, &errOut); err != nil {
		t.Fatal(err)
	}
	r, err := experiment.ReadRecipe(bytes.NewReader(exported.Bytes()))
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	path := filepath.Join(root, "recipe.json")
	if err := os.WriteFile(path, exported.Bytes(), 0600); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	args := []string{"experiment", "--recipe", path, "--recipe-sha256", r.SHA256, "--attempt", "cli-1", "--out-dir", filepath.Join(root, "artifacts")}
	if err := Run(context.Background(), args, &out, &errOut); err != nil {
		t.Fatal(err)
	}
	var a experiment.Attempt
	if err := json.Unmarshal(out.Bytes(), &a); err != nil {
		t.Fatal(err)
	}
	if a.Status != "completed" || len(a.Results) != 4 || a.Qualified {
		t.Fatal("CLI experiment incomplete")
	}
	if _, err := os.Stat(filepath.Join(root, "artifacts", "attempts", "cli-1.json")); err != nil {
		t.Fatal(err)
	}
	// A declared experiment with incomplete input retains a failed attempt and
	// gives scripts a nonzero exit instead of hiding failure in successful JSON.
	d, err := replay.FixtureDataset()
	if err != nil {
		t.Fatal(err)
	}
	d.Records = d.Records[:3]
	d, err = replay.Seal(d)
	if err != nil {
		t.Fatal(err)
	}
	r.DatasetSHA256 = d.SHA256
	r, err = experiment.Seal(r)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	datasetPath := filepath.Join(root, "incomplete.json")
	raw, err = json.Marshal(d)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(datasetPath, raw, 0600); err != nil {
		t.Fatal(err)
	}
	failedArgs := []string{"experiment", "--recipe", path, "--recipe-sha256", r.SHA256, "--dataset", datasetPath, "--attempt", "cli-failed", "--out-dir", filepath.Join(root, "artifacts")}
	out.Reset()
	if err := Run(context.Background(), failedArgs, &out, &errOut); err == nil {
		t.Fatal("failed attempt returned success")
	}
	if _, err := os.Stat(filepath.Join(root, "artifacts", "attempts", "cli-failed.json")); err != nil {
		t.Fatal("failed CLI attempt missing")
	}
	for _, args := range [][]string{{"experiment"}, {"experiment", "--export-recipe", "--attempt", "x"}, {"experiment", "--recipe", path, "--attempt", "x", "--out-dir", root}, {"experiment", "--attempt", "../x", "--out-dir", root}} {
		if err := Run(context.Background(), args, &bytes.Buffer{}, &errOut); err == nil {
			t.Fatal("invalid experiment accepted")
		}
	}
}

func TestServeRequiresAuthority(t *testing.T) {
	t.Setenv("MANTIK_API_TOKEN", "")
	t.Setenv("DATABASE_URL", "")
	for _, args := range [][]string{{"serve", "--listen", "0.0.0.0:40880"}, {"serve"}, {"demo", "extra"}, {"status", "--format", "csv"}} {
		if err := Run(context.Background(), args, &bytes.Buffer{}, &bytes.Buffer{}); err == nil {
			t.Fatalf("unexpected acceptance: %v", args)
		}
	}
}
