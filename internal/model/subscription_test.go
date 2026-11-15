package model

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestJournalCallBudgetHashesAndSanitizesEnvironment(t *testing.T) {
	root := filepath.Join(t.TempDir(), "journal")
	j, err := OpenJournal(root)
	if err != nil {
		t.Fatal(err)
	}
	j.now = func() time.Time { return time.Date(2026, 11, 15, 1, 0, 0, 0, time.UTC) }
	var seen []string
	j.run = func(_ context.Context, name, prompt string, args ...string) ([]byte, error) {
		seen = append(seen, name+" "+strings.Join(args, " "))
		return []byte("answer"), nil
	}
	first, err := j.Call(context.Background(), Call{Provider: Codex, Prompt: "research", Timeout: time.Second}, 1)
	if err != nil || first.Status != "completed" || first.Response != "answer" || first.PromptSHA256 == "research" || first.ResponseSHA256 == "answer" {
		t.Fatal(first, err)
	}
	if _, err = j.Call(context.Background(), Call{Provider: Claude, Prompt: "other", Timeout: time.Second}, 1); !errors.Is(err, ErrBudgetExhausted) {
		t.Fatal(err)
	}
	if len(seen) != 1 || !strings.Contains(seen[0], "codex exec --sandbox read-only --ephemeral --ignore-rules --ignore-user-config --skip-git-repo-check -") {
		t.Fatal(seen)
	}
	if raw, err := json.Marshal(first); err != nil || strings.Contains(string(raw), "answer") {
		t.Fatal("response escaped JSON", err)
	}
	if raw, err := os.ReadFile(filepath.Join(root, "calls.jsonl")); err != nil || (strings.Contains(string(raw), "research") || strings.Contains(string(raw), "answer")) {
		t.Fatal(string(raw), err)
	}
}

func TestJournalBudgetIsSharedAcrossConcurrentCallersAndRestart(t *testing.T) {
	root := filepath.Join(t.TempDir(), "journal")
	first, err := OpenJournal(root)
	if err != nil {
		t.Fatal(err)
	}
	second, err := OpenJournal(root)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 11, 15, 1, 0, 0, 0, time.UTC)
	first.now, second.now = func() time.Time { return now }, func() time.Time { return now }
	first.run = func(context.Context, string, string, ...string) ([]byte, error) { return []byte("one"), nil }
	second.run = first.run
	var calls, limited atomic.Int32
	var group sync.WaitGroup
	for _, journal := range []*Journal{first, second} {
		group.Add(1)
		go func(j *Journal) {
			defer group.Done()
			if _, err := j.Call(context.Background(), Call{Provider: Codex, Prompt: "x", Timeout: time.Second}, 1); errors.Is(err, ErrBudgetExhausted) || errors.Is(err, ErrBusy) {
				limited.Add(1)
			} else if err == nil {
				calls.Add(1)
			} else {
				t.Error(err)
			}
		}(journal)
	}
	group.Wait()
	if calls.Load() != 1 || limited.Load() != 1 {
		t.Fatalf("calls=%d limited=%d", calls.Load(), limited.Load())
	}
	third, err := OpenJournal(root)
	if err != nil {
		t.Fatal(err)
	}
	third.now, third.run = func() time.Time { return now }, first.run
	if _, err := third.Call(context.Background(), Call{Provider: Codex, Prompt: "again", Timeout: time.Second}, 1); !errors.Is(err, ErrBudgetExhausted) {
		t.Fatal(err)
	}
}

func TestRateLimitBackoffPersistsAndPreventsAnotherSpawn(t *testing.T) {
	root := filepath.Join(t.TempDir(), "journal")
	j, err := OpenJournal(root)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 11, 15, 1, 0, 0, 0, time.UTC)
	j.now = func() time.Time { return now }
	spawned := 0
	j.run = func(context.Context, string, string, ...string) ([]byte, error) {
		spawned++
		return []byte("429 retry later"), errors.New("429 rate limit")
	}
	if _, err := j.Call(context.Background(), Call{Provider: Codex, Prompt: "one", Timeout: time.Second}, 3); err == nil {
		t.Fatal("rate limit accepted")
	}
	if _, err := j.Call(context.Background(), Call{Provider: Codex, Prompt: "two", Timeout: time.Second}, 3); !errors.Is(err, ErrBackoff) {
		t.Fatal(err)
	}
	if spawned != 1 {
		t.Fatal(spawned)
	}
}
func TestCleanEnvRemovesCredentials(t *testing.T) {
	got := strings.Join(cleanEnv([]string{"PATH=/bin", "OPENAI_API_KEY=secret", "anthropic_api_key=secret", "ANTHROPIC_AUTH_TOKEN=secret", "OPENAI_ACCESS_TOKEN=secret"}), "|")
	if strings.Contains(got, "secret") || !strings.Contains(got, "PATH=/bin") {
		t.Fatal(got)
	}
}

func TestRunnerUsesStdinAndBoundsOutput(t *testing.T) {
	out, err := run(context.Background(), "/bin/sh", "private-prompt", "-c", "cat")
	if err != nil || string(out) != "private-prompt" {
		t.Fatal(string(out), err)
	}
	out, err = run(context.Background(), "/bin/sh", "", "-c", "head -c 70000 /dev/zero")
	if err == nil || len(out) != 65536 {
		t.Fatal(len(out), err)
	}
	_, err = run(context.Background(), "/bin/sh", "", "-c", "echo '429 too many requests' >&2; exit 1")
	if err == nil || !strings.Contains(err.Error(), "rate limit") {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err = run(ctx, "/bin/sh", "", "-c", "sleep 30 & wait")
	if !errors.Is(err, context.DeadlineExceeded) || time.Since(start) > time.Second {
		t.Fatal(err)
	}
}

func TestJournalRejectsCorruptBudgetAndSymlinks(t *testing.T) {
	for _, raw := range []string{`{"date":"broken","calls":0}`, `{"date":"2026-11-15","calls":-1}`} {
		root := filepath.Join(t.TempDir(), "journal")
		j, err := OpenJournal(root)
		if err != nil {
			t.Fatal(err)
		}
		if err = os.WriteFile(filepath.Join(root, "budget.json"), []byte(raw), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err = j.Call(context.Background(), Call{Provider: Codex, Prompt: "x", Timeout: time.Second}, 2); err == nil {
			t.Fatal("corrupt budget admitted")
		}
	}
	root := filepath.Join(t.TempDir(), "journal")
	j, err := OpenJournal(root)
	if err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(t.TempDir(), "target")
	if err = os.WriteFile(target, []byte(`{"date":"2026-11-15","calls":0}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err = os.Symlink(target, filepath.Join(root, "budget.json")); err != nil {
		t.Fatal(err)
	}
	if _, err = j.Call(context.Background(), Call{Provider: Codex, Prompt: "x", Timeout: time.Second}, 2); err == nil {
		t.Fatal("symlink budget admitted")
	}
}

func TestWeeklyLimitOnStdoutTriggersBackoff(t *testing.T) {
	out, err := run(context.Background(), "/bin/sh", "", "-c", "echo \"You've hit your weekly limit\"; exit 1")
	if err == nil || !strings.Contains(err.Error(), "rate limit") || len(out) == 0 {
		t.Fatal(err)
	}
}

func TestJournalDailyBudgetResetsNextUTCDay(t *testing.T) {
	j, err := OpenJournal(filepath.Join(t.TempDir(), "journal"))
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 11, 15, 23, 59, 0, 0, time.UTC)
	j.now = func() time.Time { return now }
	j.run = func(context.Context, string, string, ...string) ([]byte, error) { return []byte("ok"), nil }
	call := Call{Provider: Codex, Prompt: "x", Timeout: time.Second}
	if _, err = j.Call(context.Background(), call, 1); err != nil {
		t.Fatal(err)
	}
	if _, err = j.Call(context.Background(), call, 1); !errors.Is(err, ErrBudgetExhausted) {
		t.Fatal(err)
	}
	now = now.Add(2 * time.Minute)
	if _, err = j.Call(context.Background(), call, 1); err != nil {
		t.Fatal(err)
	}
	now = now.Add(-24 * time.Hour)
	if _, err = j.Call(context.Background(), call, 1); err == nil {
		t.Fatal("backward clock reset budget")
	}
}

func TestRateLimitBackoffSurvivesUTCDayReset(t *testing.T) {
	root := filepath.Join(t.TempDir(), "journal")
	j, err := OpenJournal(root)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 11, 15, 23, 59, 0, 0, time.UTC)
	j.now = func() time.Time { return now }
	spawned := 0
	runner := func(context.Context, string, string, ...string) ([]byte, error) {
		spawned++
		if spawned == 1 {
			return nil, errors.New("429 rate limit")
		}
		return []byte("ok"), nil
	}
	j.run = runner
	call := Call{Provider: Codex, Prompt: "x", Timeout: time.Second}
	record, err := j.Call(context.Background(), call, 1)
	if err == nil || record.Status != "rate_limited" {
		t.Fatal(record, err)
	}
	deadline := now.Add(5 * time.Minute)
	// Another process on the new UTC day must observe the same backoff.
	reopened, err := OpenJournal(root)
	if err != nil {
		t.Fatal(err)
	}
	reopened.now, reopened.run = func() time.Time { return now }, runner
	now = now.Add(2 * time.Minute)
	if _, err = reopened.Call(context.Background(), call, 1); !errors.Is(err, ErrBackoff) {
		t.Fatal("day reset discarded backoff", err)
	}
	if spawned != 1 {
		t.Fatal("spawned during backoff", spawned)
	}
	state, err := reopened.loadBudget()
	if err != nil || !state.RetryAfter.Equal(deadline) {
		t.Fatal(state, err)
	}
	now = deadline
	if _, err = reopened.Call(context.Background(), call, 1); err != nil {
		t.Fatal(err)
	}
	state, err = reopened.loadBudget()
	if err != nil || state.Date != "2026-11-16" || state.Calls != 1 || spawned != 2 {
		t.Fatal(state, spawned, err)
	}
	if _, err = reopened.Call(context.Background(), call, 1); !errors.Is(err, ErrBudgetExhausted) {
		t.Fatal("new-day budget not enforced", err)
	}
}
