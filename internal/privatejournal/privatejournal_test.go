package privatejournal

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestJournalKindWriteOnceAndOwnership(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "journal")
	j, err := Open(dir, true, "kind-a-v1", "a-")
	if err != nil {
		t.Fatal(err)
	}
	if err := j.WriteOnce("x.json", []byte("one"), 16); err != nil {
		t.Fatal(err)
	}
	if err := j.WriteOnce("x.json", []byte("one"), 16); err != nil {
		t.Fatal("identical retry failed", err)
	}
	if err := j.WriteOnce("x.json", []byte("two"), 16); !errors.Is(err, ErrConflict) {
		t.Fatal("existing leaf overwritten", err)
	}
	if err := j.WriteOnce("big.json", make([]byte, 17), 16); err == nil {
		t.Fatal("oversized artifact written")
	}
	if raw, err := j.Read("x.json", 16); err != nil || string(raw) != "one" {
		t.Fatal(string(raw), err)
	}
	if info, err := os.Stat(filepath.Join(dir, "x.json")); err != nil || info.Mode().Perm() != 0600 {
		t.Fatal("artifact is not private", err)
	}
	if _, err := Open(dir, false, "kind-a-v1", "a-"); !errors.Is(err, ErrBusy) {
		t.Fatal("second owner opened the journal", err)
	}
	id := j.ID
	if err := j.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(dir, false, "kind-b-v1", "b-"); err == nil {
		t.Fatal("another journal kind opened this journal")
	}
	again, err := Open(dir, false, "kind-a-v1", "a-")
	if err != nil || again.ID != id {
		t.Fatal("journal identity changed on reopen", err)
	}
	_ = again.Close()
}
