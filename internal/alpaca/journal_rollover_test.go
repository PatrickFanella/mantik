package alpaca

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func rolloverRun(t *testing.T, j *Journal, s CollectorSpec, control *testCollectorControl, receiver *testCollectorReceiver, id string) {
	t.Helper()
	client := testClient(t, func(w http.ResponseWriter, _ *http.Request) { _, _ = fmt.Fprint(w, freshQuote()) })
	s.Session.ID, s.OwnerID = id, id
	s.Session.IntervalSeconds = 1
	pause := func(ctx context.Context, _ time.Duration) error { return ctx.Err() }
	out, err := j.runCollector(context.Background(), client, s, control, receiver, pause)
	if err != nil || out.State != "completed" {
		t.Fatal(out, err)
	}
}

// A rollover exports and verifies the journal, empties its working files and
// leaves it collecting under the same registration.
func TestJournalRolloverExportsAndKeepsCollecting(t *testing.T) {
	j, s, control, receiver := collectorFixture(t)
	archives := t.TempDir()
	rolloverRun(t, j, s, control, receiver, "run-one")
	first, err := j.Rollover(context.Background(), archives)
	if err != nil || first.Sequence != 1 || first.PreviousSHA256 != "" || first.Files == 0 {
		t.Fatal(first, err)
	}
	if _, err := VerifyArchive(context.Background(), filepath.Join(archives, fmt.Sprintf("%s-%06d", j.ID, 1)), first.ArchiveSHA256); err != nil {
		t.Fatal(err)
	}
	status, err := j.Status()
	if err != nil || status.Captures != 0 || status.Inputs != 0 || status.Binding == nil {
		t.Fatal(status, err)
	}
	rolloverRun(t, j, s, control, receiver, "run-two")
	second, err := j.Rollover(context.Background(), archives)
	if err != nil || second.Sequence != 2 || second.PreviousSHA256 != first.SHA256 {
		t.Fatal(second, err)
	}
	if _, err := j.Audit(); err != nil {
		t.Fatal("audit after rollover", err)
	}
	// The second export carries the first rollover's record.
	raw, err := os.ReadFile(filepath.Join(archives, fmt.Sprintf("%s-%06d", j.ID, 2), "files", rolloverName(1)))
	if err != nil || !strings.Contains(string(raw), first.SHA256) {
		t.Fatal("chain not exported", err)
	}
}

// A crash after the record is written finishes on the next open, and an
// altered record stops the chain.
func TestJournalRolloverResumesAndDetectsTampering(t *testing.T) {
	j, s, control, receiver := collectorFixture(t)
	path := j.root.Name()
	archives := t.TempDir()
	rolloverRun(t, j, s, control, receiver, "run-one")
	if _, err := j.Rollover(context.Background(), archives); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(path, "capture-leftover-r1.json"), []byte("x"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(path, rolloverPending), []byte("{}"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := j.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := OpenExistingJournal(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = reopened.Close() }()
	if _, err := os.Stat(filepath.Join(path, "capture-leftover-r1.json")); !os.IsNotExist(err) {
		t.Fatal("pending rollover not finished", err)
	}
	if _, err := os.Stat(filepath.Join(path, rolloverPending)); !os.IsNotExist(err) {
		t.Fatal("pending marker kept", err)
	}
	record := filepath.Join(path, rolloverName(1))
	raw, err := os.ReadFile(record)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(record, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(record, []byte(strings.Replace(string(raw), `"files":`, `"files":9`, 1)), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := reopened.Rollover(context.Background(), archives); !errors.Is(err, ErrJournalConflict) {
		t.Fatal("tampered chain accepted", err)
	}
}

func TestJournalRolloverRefusesUnacknowledgedInputs(t *testing.T) {
	j := testOpenJournal(t, filepath.Join(t.TempDir(), "journal"))
	if _, err := j.Retain(recording(goodQuotes)); err != nil {
		t.Fatal(err)
	}
	if _, err := j.Rollover(context.Background(), t.TempDir()); err == nil || !strings.Contains(err.Error(), "acknowledged") {
		t.Fatal(err)
	}
	if status, err := j.Status(); err != nil || status.Captures != 1 {
		t.Fatal("refused rollover removed evidence", status, err)
	}
}
