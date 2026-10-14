package alpaca

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestArchiveRestorePreservesDeliveryAndAllRetainedBytes(t *testing.T) {
	base := t.TempDir()
	source := filepath.Join(base, "journal")
	j := testOpenJournal(t, source)
	r := recording(goodQuotes)
	if _, err := j.Retain(r); err != nil {
		t.Fatal(err)
	}
	receiver := newJournalReceiver(t)
	binding := Binding{APIURL: "http://127.0.0.1:40880", ReceiverID: ""}
	// ReceiverID is obtained through the same actual store readback as delivery.
	snapshot, err := receiver.Snapshot(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	binding.ReceiverID = snapshot.ID
	if _, err := j.Flush(context.Background(), binding, receiver, 1); err != nil {
		t.Fatal(err)
	}
	pending := r
	pending.CaptureID = "pending-second"
	if _, err := j.Retain(pending); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "tmp-interrupted"), []byte{0xff, 0, 1}, 0600); err != nil {
		t.Fatal(err)
	}
	before, err := j.Audit()
	if err != nil {
		t.Fatal(err)
	}
	archivePath := filepath.Join(base, "archive")
	archive, err := j.Archive(context.Background(), archivePath)
	if err != nil || archive.SHA256 == "" || archive.JournalID != j.ID {
		t.Fatal(archive, err)
	}
	restoredPath := filepath.Join(base, "restored")
	restored, err := RestoreArchive(context.Background(), archivePath, archive.SHA256, restoredPath)
	if err != nil || !restored.BytesVerified || !restored.ContractsVerified || !reflect.DeepEqual(*restored.Audit, before) {
		t.Fatal(restored, err)
	}
	names, err := directoryNames(j.root)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range names {
		if name == "lock" {
			continue
		}
		original, err := os.ReadFile(filepath.Join(source, name))
		if err != nil {
			t.Fatal(err)
		}
		copied, err := os.ReadFile(filepath.Join(restoredPath, name))
		if err != nil {
			t.Fatal(err)
		}
		if string(original) != string(copied) {
			t.Fatal("restore changed retained bytes", name)
		}
	}
	if _, err := os.Stat(filepath.Join(archivePath, "files", "lock")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("process lock was archived")
	}
	copy := testOpenJournal(t, restoredPath)
	beforePosts := receiver.posts
	if _, err := copy.Flush(context.Background(), Binding{APIURL: "http://127.0.0.1:49999", ReceiverID: binding.ReceiverID}, receiver, 1); !errors.Is(err, ErrTargetChanged) || receiver.posts != beforePosts {
		t.Fatal("restore rebound receiver", err)
	}
	delivered, err := copy.Flush(context.Background(), binding, receiver, 1)
	if err != nil || delivered.Acknowledged != 2 || receiver.posts != beforePosts+1 {
		t.Fatal("restore lost ack/pending history", delivered, err)
	}
}
func TestArchiveRestoreRetainsFailedAndIncompleteCollectionsAndSessions(t *testing.T) {
	base := t.TempDir()
	j := testOpenJournal(t, filepath.Join(base, "journal"))
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(401)
		_, _ = fmt.Fprint(w, "manufactured failure")
	})
	s := sessionSpec()
	if _, err := j.RunSession(context.Background(), c, s); !errors.Is(err, ErrSessionStopped) {
		t.Fatal(err)
	}
	incomplete := collectionSpec()
	incomplete.ID = "incomplete"
	if err := j.writeCollection(collectionName(incomplete.ID, "intent"), collectionIntent{"mantik-alpaca-collection-v1", j.ID, incomplete, time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	session := sessionSpec()
	session.ID = "unfinished-session"
	if err := j.writeCollection(sessionName(session.ID, "intent"), sessionIntent{"mantik-alpaca-session-v1", j.ID, session, time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	receipt, err := j.Archive(context.Background(), filepath.Join(base, "archive"))
	if err != nil {
		t.Fatal(err)
	}
	result, err := RestoreArchive(context.Background(), receipt.Path, receipt.SHA256, filepath.Join(base, "copy"))
	if err != nil || result.Audit.FailedCollections != 1 || result.Audit.IncompleteCollections != 1 || result.Audit.StoppedSessions != 1 || result.Audit.IncompleteSessions != 1 {
		t.Fatal(result, err)
	}
}
func TestArchiveRejectsTamperMissingExtraSymlinkAndWrongDigest(t *testing.T) {
	for _, fault := range []string{"digest", "tamper", "missing", "extra", "symlink", "permissions", "directory_link"} {
		t.Run(fault, func(t *testing.T) {
			base := t.TempDir()
			j := testOpenJournal(t, filepath.Join(base, "source"))
			receipt, err := j.Archive(context.Background(), filepath.Join(base, "archive"))
			if err != nil {
				t.Fatal(err)
			}
			leaf := filepath.Join(receipt.Path, "files", "journal.json")
			switch fault {
			case "digest":
				receipt.SHA256 = strings.Repeat("0", 64)
			case "tamper":
				if err := os.WriteFile(leaf, []byte("changed"), 0600); err != nil {
					t.Fatal(err)
				}
			case "missing":
				if err := os.Remove(leaf); err != nil {
					t.Fatal(err)
				}
			case "extra":
				if err := os.WriteFile(filepath.Join(receipt.Path, "files", "extra"), []byte("extra"), 0600); err != nil {
					t.Fatal(err)
				}
			case "symlink":
				if err := os.Remove(leaf); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(filepath.Join(base, "source", "journal.json"), leaf); err != nil {
					t.Fatal(err)
				}
			case "permissions":
				if err := os.Chmod(leaf, 0644); err != nil {
					t.Fatal(err)
				}
			case "directory_link":
				if err := os.Rename(filepath.Join(receipt.Path, "files"), filepath.Join(base, "moved")); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(filepath.Join(base, "moved"), filepath.Join(receipt.Path, "files")); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := VerifyArchive(context.Background(), receipt.Path, receipt.SHA256); err == nil {
				t.Fatal("invalid archive verified")
			}
			destination := filepath.Join(base, "copy")
			if _, err := RestoreArchive(context.Background(), receipt.Path, receipt.SHA256, destination); err == nil {
				t.Fatal("invalid archive restored")
			}
			if _, err := os.Stat(destination); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("invalid preflight created restore")
			}
		})
	}
}
func TestArchiveAndRestoreNeverOverwriteOrNestOrWeakenPrivacy(t *testing.T) {
	base := t.TempDir()
	source := filepath.Join(base, "journal")
	j := testOpenJournal(t, source)
	if _, err := j.Archive(context.Background(), filepath.Join(source, "nested")); err == nil {
		t.Fatal("archive nested inside source")
	}
	if _, err := os.Stat(filepath.Join(source, "nested")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("nested archive created")
	}
	receipt, err := j.Archive(context.Background(), filepath.Join(base, "archive"))
	if err != nil {
		t.Fatal(err)
	}
	original, err := os.ReadFile(filepath.Join(receipt.Path, "archive.json"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := j.Archive(context.Background(), receipt.Path); err == nil {
		t.Fatal("existing archive overwritten")
	}
	if _, err := RestoreArchive(context.Background(), receipt.Path, receipt.SHA256, source); err == nil {
		t.Fatal("existing source overwritten")
	}
	if _, err := RestoreArchive(context.Background(), receipt.Path, receipt.SHA256, filepath.Join(receipt.Path, "nested")); err == nil {
		t.Fatal("restore nested in archive")
	}
	current, err := os.ReadFile(filepath.Join(receipt.Path, "archive.json"))
	if err != nil || string(current) != string(original) {
		t.Fatal("archive changed on retry", err)
	}
	for _, path := range []string{receipt.Path, filepath.Join(receipt.Path, "files")} {
		info, err := os.Stat(path)
		if err != nil || info.Mode().Perm() != 0700 {
			t.Fatal("directory privacy changed", err)
		}
	}
	info, err := os.Stat(filepath.Join(receipt.Path, "archive.json"))
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatal("manifest privacy changed", err)
	}
}
func TestArchiveRetainsCorruptionButRestoreStaysGuarded(t *testing.T) {
	base := t.TempDir()
	source := filepath.Join(base, "source")
	j := testOpenJournal(t, source)
	r := recording(goodQuotes)
	if _, err := j.Retain(r); err != nil {
		t.Fatal(err)
	}
	broken := []byte("invalid retained capture")
	if err := os.WriteFile(filepath.Join(source, captureName(r)), broken, 0600); err != nil {
		t.Fatal(err)
	}
	receipt, err := j.Archive(context.Background(), filepath.Join(base, "archive"))
	if err != nil {
		t.Fatal("physical evidence not preserved", err)
	}
	destination := filepath.Join(base, "failed-copy")
	result, err := RestoreArchive(context.Background(), receipt.Path, receipt.SHA256, destination)
	if !errors.Is(err, ErrRestoreContract) || !result.BytesVerified || result.ContractsVerified {
		t.Fatal(result, err)
	}
	raw, err := os.ReadFile(filepath.Join(destination, captureName(r)))
	if err != nil || string(raw) != string(broken) {
		t.Fatal("failed restore evidence lost", err)
	}
	for _, open := range []func(string) (*Journal, error){OpenJournal, OpenExistingJournal} {
		if _, err := open(destination); !errors.Is(err, ErrRestorePending) {
			t.Fatal("unverified restored journal opened", err)
		}
	}
}
func TestArchiveBoundsMalformedManifestAndCancellation(t *testing.T) {
	base := t.TempDir()
	j := testOpenJournal(t, filepath.Join(base, "source"))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	destination := filepath.Join(base, "cancelled")
	if _, err := j.Archive(ctx, destination); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if _, err := os.Stat(destination); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("cancelled archive created")
	}
	receipt, err := j.Archive(context.Background(), filepath.Join(base, "archive"))
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(receipt.Path, "archive.json"))
	if err != nil {
		t.Fatal(err)
	}
	var manifest ArchiveManifest
	if err := json.Unmarshal(raw, &manifest); err != nil {
		t.Fatal(err)
	}
	manifest.TotalBytes = MaxArchiveBytes + 1
	malformed, _ := json.Marshal(manifest)
	if err := os.WriteFile(filepath.Join(receipt.Path, "archive.json"), malformed, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := VerifyArchive(context.Background(), receipt.Path, Digest(string(malformed))); err == nil {
		t.Fatal("oversized manifest admitted")
	}
}
func TestArchivePhysicalPreflightPreservesSourceOnInvalidLeaf(t *testing.T) {
	for _, kind := range []string{"public", "symlink", "directory", "oversize"} {
		t.Run(kind, func(t *testing.T) {
			base := t.TempDir()
			source := filepath.Join(base, "source")
			j := testOpenJournal(t, source)
			leaf := filepath.Join(source, "evidence")
			switch kind {
			case "public":
				if err := os.WriteFile(leaf, []byte("evidence"), 0644); err != nil {
					t.Fatal(err)
				}
			case "symlink":
				if err := os.Symlink("journal.json", leaf); err != nil {
					t.Fatal(err)
				}
			case "directory":
				if err := os.Mkdir(leaf, 0700); err != nil {
					t.Fatal(err)
				}
			case "oversize":
				f, err := os.OpenFile(leaf, os.O_CREATE|os.O_WRONLY, 0600)
				if err != nil {
					t.Fatal(err)
				}
				if err := f.Truncate(maxJournalFile + 1); err != nil {
					t.Fatal(err)
				}
				_ = f.Close()
			}
			destination := filepath.Join(base, "archive")
			if _, err := j.Archive(context.Background(), destination); err == nil {
				t.Fatal("invalid private leaf archived")
			}
			if _, err := os.Stat(destination); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("bad preflight created archive")
			}
			if _, err := os.Lstat(leaf); err != nil {
				t.Fatal("source artifact removed", err)
			}
		})
	}
}

type archiveCancelContext struct {
	context.Context
	cancel    context.CancelFunc
	calls     int
	threshold int
}

func (c *archiveCancelContext) Err() error {
	c.calls++
	if c.calls == c.threshold {
		c.cancel()
	}
	return c.Context.Err()
}
func TestRestoreCancellationLeavesBlockingGuardAndRetainsPartialCopy(t *testing.T) {
	for _, threshold := range []int{3, 4} {
		t.Run(fmt.Sprint(threshold), func(t *testing.T) {
			base := t.TempDir()
			j := testOpenJournal(t, filepath.Join(base, "source"))
			receipt, err := j.Archive(context.Background(), filepath.Join(base, "archive"))
			if err != nil {
				t.Fatal(err)
			}
			parent, cancel := context.WithCancel(context.Background())
			defer cancel()
			ctx := &archiveCancelContext{Context: parent, cancel: cancel, threshold: threshold}
			destination := filepath.Join(base, "cancelled-copy")
			if _, err := RestoreArchive(ctx, receipt.Path, receipt.SHA256, destination); !errors.Is(err, context.Canceled) {
				t.Fatal("restore fault did not cancel", err)
			}
			if _, err := OpenExistingJournal(destination); !errors.Is(err, ErrRestorePending) {
				t.Fatal("interrupted copy became usable", err)
			}
			if _, err := os.Stat(filepath.Join(destination, "restore-pending")); err != nil {
				t.Fatal("guard not retained", err)
			}
			if _, err := VerifyArchive(context.Background(), receipt.Path, receipt.SHA256); err != nil {
				t.Fatal("source archive changed on cancellation", err)
			}
		})
	}
}
func TestJournalReaderAndLockRejectInternalSymlinkLeaves(t *testing.T) {
	root := filepath.Join(t.TempDir(), "journal")
	j := testOpenJournal(t, root)
	if err := os.Symlink("journal.json", filepath.Join(root, "linked-evidence")); err != nil {
		t.Fatal(err)
	}
	if _, err := j.read("linked-evidence", 4096); err == nil {
		t.Fatal("reader followed internal symlink")
	}
	_ = j.Close()
	if err := os.Remove(filepath.Join(root, "lock")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("journal.json", filepath.Join(root, "lock")); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenExistingJournal(root); err == nil {
		t.Fatal("lock followed internal symlink")
	}
}
