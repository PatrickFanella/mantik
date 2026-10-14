package service

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/domain"
	"git.subcult.tv/subculture-collective/mantik/internal/storage"
	"git.subcult.tv/subculture-collective/mantik/internal/workflow"
)

func waitGate(t *testing.T, directory string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if data, err := os.ReadFile(filepath.Join(directory, "reached.json")); err == nil && len(data) > 0 {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("delivery did not reach fixture gate")
}

func TestFixtureGateCrashBoundaries(t *testing.T) {
	for _, stage := range []string{"before_submit", "after_submit"} {
		t.Run(stage, func(t *testing.T) {
			directory := t.TempDir()
			gate, err := NewFixtureActionGate(directory, stage)
			if err != nil {
				t.Fatal(err)
			}
			store := storage.NewQueuedMemory()
			quote := workflow.Fixtures()[1]
			if _, err := store.Ingest(context.Background(), quote); err != nil {
				t.Fatal(err)
			}
			clock := newFakeClock(quote.ReceivedAt)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			done := make(chan error, 1)
			go func() { done <- runActionCycle(ctx, store, "crashed", clock, 30*time.Second, gate) }()
			waitGate(t, directory)
			before, err := store.Snapshot(context.Background())
			if err != nil || before.State.Case.Entry != nil {
				t.Fatal("gate did not stop before acknowledgment", err)
			}
			for _, action := range before.State.Actions.Items {
				if action.Status != "unknown" {
					t.Fatal("submission boundary not retained")
				}
			}
			cancel()
			if err := <-done; !errors.Is(err, context.Canceled) {
				t.Fatal("gate did not drain on cancellation", err)
			}
			clock.mu.Lock()
			clock.at = quote.ReceivedAt.Add(31 * time.Second)
			clock.mu.Unlock()
			if err := RunActionCycle(context.Background(), store, "restarted", clock, 30*time.Second); err != nil {
				t.Fatal(err)
			}
			after, err := store.Snapshot(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			want := "expired"
			if stage == "after_submit" {
				want = "completed"
			}
			for _, action := range after.State.Actions.Items {
				if action.Status != want {
					t.Fatalf("got %s, want %s", action.Status, want)
				}
			}
			if (after.State.Case.Entry != nil) != (stage == "after_submit") {
				t.Fatal("pre/post-effect recovery confused")
			}
			if err := RunActionCycle(context.Background(), store, "again", clock, 30*time.Second); err != nil {
				t.Fatal(err)
			}
			last, err := store.Snapshot(context.Background())
			if err != nil || len(last.Events) != len(after.Events) {
				t.Fatal("recovery duplicated effect or evidence", err)
			}
		})
	}
}

func TestFixtureGateReleaseRechecksChangedAuthority(t *testing.T) {
	directory := t.TempDir()
	gate, err := NewFixtureActionGate(directory, "before_submit")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	store := storage.NewQueuedMemory()
	quote := workflow.Fixtures()[1]
	if _, err := store.Ingest(ctx, quote); err != nil {
		t.Fatal(err)
	}
	clock := newFakeClock(quote.ReceivedAt)
	done := make(chan error, 1)
	go func() { done <- runActionCycle(ctx, store, "gated", clock, 30*time.Second, gate) }()
	waitGate(t, directory)
	quote.ID = "changed-authority"
	quote.PriceCents = 10300
	quote.OccurredAt = quote.OccurredAt.Add(time.Second)
	quote.ReceivedAt = quote.OccurredAt
	if _, err := store.Ingest(ctx, quote); err != nil {
		t.Fatal(err)
	}
	clock.mu.Lock()
	clock.at = quote.ReceivedAt
	clock.mu.Unlock()
	if err := os.WriteFile(filepath.Join(directory, "release"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	snapshot, err := store.Snapshot(ctx)
	if err != nil || snapshot.State.Case.Entry != nil {
		t.Fatal("changed quote authority executed", err)
	}
	for _, action := range snapshot.State.Actions.Items {
		if action.Status != "rejected" {
			t.Fatal("changed authority not rejected")
		}
	}
}

func TestFixtureGateConfigurationAndEvidenceGuards(t *testing.T) {
	if gate, err := NewFixtureActionGate("", ""); err != nil || gate != nil {
		t.Fatal("default service has a gate")
	}
	for _, config := range [][2]string{{"relative", "before_submit"}, {t.TempDir(), "invalid"}, {"", "after_submit"}, {filepath.Join(t.TempDir(), "absent"), "before_submit"}} {
		if _, err := NewFixtureActionGate(config[0], config[1]); err == nil {
			t.Fatal("invalid fixture gate accepted")
		}
	}
	directory := t.TempDir()
	gate, err := NewFixtureActionGate(directory, "before_submit")
	if err != nil {
		t.Fatal(err)
	}
	if err := gate.pause(context.Background(), "before_submit", domain.ActionIntent{}); err == nil {
		t.Fatal("non-fixture intent admitted")
	}
	marker := filepath.Join(directory, "reached.json")
	if err := os.WriteFile(marker, []byte("original"), 0o600); err != nil {
		t.Fatal(err)
	}
	intent := domain.ActionIntent{Version: "paper-action-v1", Quote: domain.Observation{Source: "fixture", Symbol: "DEMO"}}
	if err := gate.pause(context.Background(), "before_submit", intent); !errors.Is(err, os.ErrExist) {
		t.Fatal("existing evidence replaced", err)
	}
	if data, err := os.ReadFile(marker); err != nil || string(data) != "original" {
		t.Fatal("marker changed", err)
	}
}
