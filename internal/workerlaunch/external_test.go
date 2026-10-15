package workerlaunch

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"git.subcult.tv/subculture-collective/mantik/internal/experiment"
	"git.subcult.tv/subculture-collective/mantik/internal/externalplan"
	"git.subcult.tv/subculture-collective/mantik/internal/replay"
)

func externalFixture(t *testing.T) (externalplan.Job, map[string][]byte) {
	t.Helper()
	native, input := fixture(t)
	// Build a genuine native reference, not an external engine assertion.
	d, err := replay.Read(bytes.NewReader(input))
	if err != nil {
		t.Fatal(err)
	}
	n, err := experiment.Execute(context.Background(), "reference", native.Recipe, d)
	if err != nil {
		t.Fatal(err)
	}
	recipe, _ := json.Marshal(native.Recipe)
	reference, _ := json.Marshal(n)
	inputs := map[string][]byte{"dataset.json": input, "recipe.json": recipe, "native.json": reference}
	j, err := externalplan.Declare("external-job", native.ImageSHA256, strings.Repeat("b", 64), strings.Repeat("c", 64), inputs)
	if err != nil {
		t.Fatal(err)
	}
	return j, inputs
}
func TestExternalHostLifecycleAndNativeVersionSeparation(t *testing.T) {
	j, inputs := externalFixture(t)
	p := externalExecution(j)
	root := filepath.Join(t.TempDir(), "attempt")
	d := &fakeDocker{t: t, profile: &p}
	d.waitHook = func() { d.s.State.ExitCode = 1 }
	// This test isolates host classification from the separately tested result
	// validator; actual Python results are exercised by the container rehearsal.
	p.outcome = func([]byte) (outcome, error) {
		return outcome{sha: strings.Repeat("d", 64), workerStatus: "compared", status: "mismatch", reason: "comparison_metrics_differ", exit: 1, eligible: true}, nil
	}
	r, err := launchExecution(context.Background(), root, p, inputs, j.ImageSHA256, d)
	if err != nil || r.Schema != "mantik-external-worker-host-receipt-v1" || r.Status != "mismatch" || !r.ProfileVerified || !r.HostDeadlineMet || d.starts != 1 {
		t.Fatal(r, err)
	}
	repeated, err := reconcileExternal(context.Background(), root, j.ImageSHA256, false, d)
	if err != nil || repeated.SHA256 != r.SHA256 || d.starts != 1 {
		t.Fatal("external attempt changed", err)
	}
	if _, err := reconcile(context.Background(), root, j.ImageSHA256, true, d); err == nil || d.kills != 0 {
		t.Fatal("native API accepted external acquisition")
	}
	if _, err := launchExternal(context.Background(), root, j, inputs, j.ImageSHA256, d); !errors.Is(err, ErrConflict) {
		t.Fatal("external attempt reused", err)
	}
	inputs["native.json"] = append(inputs["native.json"], ' ')
	if err := os.Chmod(root+"/input/native.json", 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(root+"/input/native.json", inputs["native.json"], 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := reconcileExternal(context.Background(), root, j.ImageSHA256, true, d); !errors.Is(err, ErrConflict) {
		t.Fatal("changed input accepted", err)
	}
}
func TestExternalInterruptedHostAndProfileFence(t *testing.T) {
	j, inputs := externalFixture(t)
	p := externalExecution(j)
	root := filepath.Join(t.TempDir(), "attempt")
	d := &fakeDocker{t: t, profile: &p, interrupt: true}
	func() {
		defer func() {
			if recover() == nil {
				t.Fatal("missing host interruption")
			}
		}()
		_, _ = launchExternal(context.Background(), root, j, inputs, j.ImageSHA256, d)
	}()
	if _, err := reconcileExternal(context.Background(), root, j.ImageSHA256, false, d); !errors.Is(err, ErrRunning) || d.kills != 0 || d.starts != 1 {
		t.Fatal("live attempt stopped or recreated", err)
	}
	d.s.Config.Env = append(d.s.Config.Env, "SECRET=forbidden")
	if _, err := reconcileExternal(context.Background(), root, j.ImageSHA256, true, d); !errors.Is(err, ErrConflict) || d.kills != 0 {
		t.Fatal("changed profile trusted", err)
	}
	d.s.Config.Env = externalExecution(j).env
	r, err := reconcileExternal(context.Background(), root, j.ImageSHA256, true, d)
	if err != nil || r.Status != "cancelled" || d.kills != 1 || d.starts != 1 {
		t.Fatal(r, err)
	}
	for _, name := range []string{"dataset.json", "recipe.json", "native.json"} {
		info, err := os.Stat(root + "/input/" + name)
		if err != nil || info.Mode().Perm() != 0444 {
			t.Fatal(name, err)
		}
	}
}
