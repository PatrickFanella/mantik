package experiment

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/library"
	"git.subcult.tv/subculture-collective/mantik/internal/replay"
)

func fixture(t *testing.T) (Recipe, replay.Dataset) {
	t.Helper()
	d, err := replay.FixtureDataset()
	if err != nil {
		t.Fatal(err)
	}
	r, err := DefaultRecipe(d)
	if err != nil {
		t.Fatal(err)
	}
	return r, d
}

func TestRegisteredBaselinesAndControls(t *testing.T) {
	r, d := fixture(t)
	registry, err := Registry()
	if err != nil {
		t.Fatal(err)
	}
	for _, pin := range append(append([]MethodPin{}, r.Methods...), r.Benchmarks...) {
		if _, err := registry.Resolve(pin.ID, library.ContractVersion); err != nil {
			t.Fatal(err)
		}
	}
	a, err := Execute(context.Background(), "attempt-1", r, d)
	if err != nil {
		t.Fatal(err)
	}
	if a.Status != "completed" || a.Qualified || len(a.Results) != 4 {
		t.Fatalf("unexpected attempt: %+v", a)
	}
	for i, want := range []int64{-6000, -3000, 0, -7200} {
		result := a.Results[i]
		if result.Metrics.MarkedPnLCents != want || result.Metrics.Observations != 4 {
			t.Fatalf("method %s metrics: %+v", result.MethodID, result.Metrics)
		}
		if i < 2 && result.Metrics.Actions != 2 || i >= 2 && result.Metrics.Actions != 0 {
			t.Fatal("benchmark created economic actions")
		}
	}
	again, err := Execute(context.Background(), "attempt-1", r, d)
	if err != nil || !reflect.DeepEqual(a, again) {
		t.Fatal("identical experiment diverged")
	}
	if a.Results[0].Replay.Frames[0].Snapshot.State.Case.StrategyVersionID == a.Results[1].Replay.Frames[0].Snapshot.State.Case.StrategyVersionID {
		t.Fatal("methods did not bind distinct strategy versions")
	}
}

func TestRecipePinsAndDeclaration(t *testing.T) {
	for _, name := range []string{"method", "parameters", "foreign_method", "benchmark", "dataset", "metrics", "capital", "cost", "window", "keys", "hash"} {
		t.Run(name, func(t *testing.T) {
			r, _ := fixture(t)
			switch name {
			case "method":
				r.Methods[0].SHA256 = strings.Repeat("0", 64)
			case "parameters":
				r.Methods[0].Definition.Strategy.Quantity++
			case "foreign_method":
				r.Methods[0].ID = "unknown-method-v1"
			case "benchmark":
				r.Benchmarks = r.Benchmarks[:1]
			case "dataset":
				r.DatasetSHA256 = ""
			case "metrics":
				r.PlannedMetrics = []string{"sharpe"}
			case "capital":
				r.CapitalCents++
			case "cost":
				r.CostModel = "free-money-v1"
			case "window":
				r.WindowStart = r.WindowEnd.Add(time.Second)
			case "keys":
				r.RequiredKeys = append(r.RequiredKeys, r.RequiredKeys[0])
			case "hash":
				r.SHA256 = strings.Repeat("0", 64)
			}
			if err := r.Validate(); err == nil {
				t.Fatal("changed declaration admitted")
			}
		})
	}
	r, _ := fixture(t)
	raw, _ := json.Marshal(r)
	if _, err := ReadRecipe(strings.NewReader(string(raw))); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadRecipe(strings.NewReader(string(raw) + " {}")); err == nil {
		t.Fatal("trailing recipe accepted")
	}
	method, err := MethodByID(library.HalfSizeID)
	if err != nil {
		t.Fatal(err)
	}
	method.Strategy.Quantity++
	if _, err := library.ResolveStrategy(*method.Strategy); err == nil {
		t.Fatal("changed executable configuration admitted")
	}
}

func TestFailuresAreRetained(t *testing.T) {
	for _, name := range []string{"pin", "required_key", "outside_window", "missing", "quarantine", "correction", "stale", "cancelled", "hash"} {
		t.Run(name, func(t *testing.T) {
			r, d := fixture(t)
			ctx := context.Background()
			switch name {
			case "pin":
				r.DatasetSHA256 = strings.Repeat("0", 64)
			case "required_key":
				r.RequiredKeys[0] = "unexpected-source-key"
			case "outside_window":
				d.Records[3].AvailableAt = r.WindowEnd.Add(time.Second)
			case "missing":
				d.Records = d.Records[:3]
			case "quarantine":
				d.Records[1].Quality, d.Records[1].Reason = "quarantined", "fixture gap"
			case "correction":
				d.Records[1].Revision = 2
			case "stale":
				d.Records[1].AvailableAt = d.Records[1].AvailableAt.Add(61 * time.Second)
			case "cancelled":
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			case "hash":
				d.SHA256 = strings.Repeat("0", 64)
			}
			if name != "pin" && name != "cancelled" && name != "hash" {
				var err error
				d, err = replay.Seal(d)
				if err != nil {
					t.Fatal(err)
				}
				r.DatasetSHA256 = d.SHA256
			}
			var err error
			r, err = Seal(r)
			if err != nil {
				t.Fatal(err)
			}
			root := t.TempDir()
			a, err := RunAndRetain(ctx, root, "failed-1", r, d)
			if err != nil {
				t.Fatal(err)
			}
			if a.Status != "failed" || a.ErrorCode == "" || a.Qualified || len(a.Results) != 0 {
				t.Fatalf("missing failure: %+v", a)
			}
			raw, err := os.ReadFile(filepath.Join(root, "attempts", "failed-1.json"))
			if err != nil {
				t.Fatal(err)
			}
			var retained Attempt
			if err := json.Unmarshal(raw, &retained); err != nil || !reflect.DeepEqual(a, retained) {
				t.Fatal("failure evidence not retained")
			}
			if _, err := os.Stat(filepath.Join(root, "recipes", r.SHA256+".json")); err != nil {
				t.Fatal("declaration missing")
			}
			if _, err := os.Stat(filepath.Join(root, "inputs", a.InputSHA256+".json")); err != nil {
				t.Fatal("failure input evidence missing")
			}
		})
	}
}

func TestConcurrentRetentionAndConflicts(t *testing.T) {
	r, d := fixture(t)
	root := t.TempDir()
	var wg sync.WaitGroup
	for range 6 {
		wg.Go(func() {
			if _, err := RunAndRetain(context.Background(), root, "same-attempt", r, d); err != nil {
				t.Errorf("retry: %v", err)
			}
		})
	}
	wg.Wait()
	files, err := os.ReadDir(filepath.Join(root, "attempts"))
	if err != nil || len(files) != 1 {
		t.Fatal("duplicate attempt artifacts")
	}
	original, err := os.ReadFile(filepath.Join(root, "attempts", "same-attempt.json"))
	if err != nil {
		t.Fatal(err)
	}
	d.SHA256 = strings.Repeat("0", 64)
	if _, err := RunAndRetain(context.Background(), root, "same-attempt", r, d); err == nil {
		t.Fatal("conflicting attempt replaced success")
	}
	after, _ := os.ReadFile(filepath.Join(root, "attempts", "same-attempt.json"))
	if string(after) != string(original) {
		t.Fatal("retained success was overwritten")
	}
	info, _ := os.Stat(filepath.Join(root, "attempts", "same-attempt.json"))
	if info.Mode().Perm() != 0600 {
		t.Fatal("artifact file is not private")
	}
}

func TestWindowMarkDoesNotInventLiquidation(t *testing.T) {
	_, d := fixture(t)
	d.Records = d.Records[:3]
	var err error
	d, err = replay.Seal(d)
	if err != nil {
		t.Fatal(err)
	}
	r, err := DefaultRecipe(d)
	if err != nil {
		t.Fatal(err)
	}
	a, err := Execute(context.Background(), "open-window", r, d)
	if err != nil {
		t.Fatal(err)
	}
	if a.Status != "completed" || a.Results[0].Metrics.Actions != 1 || a.Results[0].Metrics.RealizedPnLCents != 0 || a.Results[0].Metrics.MarkedPnLCents != 3000 {
		t.Fatal("window end invented a fill or lost unrealized mark")
	}
	last := a.Results[0].Replay.Frames[len(a.Results[0].Replay.Frames)-1]
	if last.Snapshot.State.Case.Status != "open" || last.Snapshot.State.Case.Exit != nil {
		t.Fatal("experiment liquidation changed strategy decisions")
	}
}

func TestRetentionRejectsSymlinksAndInvalidRequests(t *testing.T) {
	r, d := fixture(t)
	root := t.TempDir()
	if _, err := Execute(context.Background(), "../bad", r, d); err == nil {
		t.Fatal("path-like attempt id admitted")
	}
	bad := r
	bad.SHA256 = "invalid"
	if _, err := RunAndRetain(context.Background(), root, "bad-recipe", bad, d); err == nil {
		t.Fatal("invalid declaration retained as an experiment")
	}
	link := filepath.Join(root, "linked-root")
	if err := os.Symlink(root, link); err != nil {
		t.Fatal(err)
	}
	if _, err := RunAndRetain(context.Background(), link, "linked", r, d); err == nil {
		t.Fatal("symlink artifact directory admitted")
	}
	if err := os.MkdirAll(filepath.Join(root, "attempts"), 0700); err != nil {
		t.Fatal(err)
	}
	victim := filepath.Join(root, "victim.json")
	if err := os.WriteFile(victim, []byte("preserved"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(victim, filepath.Join(root, "attempts", "linked-attempt.json")); err != nil {
		t.Fatal(err)
	}
	if _, err := RunAndRetain(context.Background(), root, "linked-attempt", r, d); err == nil {
		t.Fatal("symlink artifact target admitted")
	}
	raw, err := os.ReadFile(victim)
	if err != nil || string(raw) != "preserved" {
		t.Fatal("existing artifact target changed")
	}
}
