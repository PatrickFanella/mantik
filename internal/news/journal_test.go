package news

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

func testJournal(t *testing.T) (*Journal, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "journal")
	j, err := OpenJournal(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = j.Close() })
	return j, path
}
func successor(r Recording, receipt RetentionReceipt, kind string) Recording {
	r.Revision++
	r.Kind = kind
	r.AvailableAt = r.AvailableAt.Add(time.Hour)
	r.ReceivedAt = r.ReceivedAt.Add(time.Hour)
	r.Supersedes = &RevisionRef{SourceID: r.SourceID, ArticleID: r.ArticleID, Revision: r.Revision - 1, RecordingSHA256: receipt.RecordingSHA256}
	if kind == "retraction" {
		r.Claims[0].Assessment = "retracted"
	}
	return r
}

func TestNewsJournalRetentionConflictAndLineage(t *testing.T) {
	j, path := testJournal(t)
	r := Fixture()
	first, err := j.Retain(r)
	if err != nil || first.Duplicate {
		t.Fatalf("retain: %+v %v", first, err)
	}
	file := filepath.Join(path, "record-"+first.Key+".json")
	bytesBefore, _ := os.ReadFile(file)
	if info, err := os.Stat(file); err != nil || info.Mode().Perm() != 0600 {
		t.Fatal("record is not private")
	}
	zone := time.FixedZone("offset", -6*3600)
	same := r
	same.PublishedAt = same.PublishedAt.In(zone)
	same.AvailableAt = same.AvailableAt.In(zone)
	same.ReceivedAt = same.ReceivedAt.In(zone)
	retry, err := j.Retain(same)
	if err != nil || !retry.Duplicate || retry.Key != first.Key {
		t.Fatalf("retry: %+v %v", retry, err)
	}
	changed := Fixture()
	changed.Claims[0].Assertion = "Changed operator assertion"
	if _, err := j.Retain(changed); !errors.Is(err, ErrNewsConflict) {
		t.Fatal("changed content replaced an identity", err)
	}
	bytesAfter, _ := os.ReadFile(file)
	if string(bytesBefore) != string(bytesAfter) {
		t.Fatal("retry/conflict rewrote retained record")
	}
	missing := successor(Fixture(), first, "correction")
	missing.ArticleID = "missing"
	missing.Supersedes.ArticleID = "missing"
	if _, err := j.Retain(missing); !errors.Is(err, ErrNewsLineage) {
		t.Fatal("missing parent accepted", err)
	}
	backwards := successor(Fixture(), first, "correction")
	backwards.AvailableAt = r.AvailableAt.Add(-time.Second)
	backwards.ReceivedAt = r.ReceivedAt
	if _, err := j.Retain(backwards); !errors.Is(err, ErrNewsLineage) {
		t.Fatal("backward source availability accepted", err)
	}
	wrong := successor(Fixture(), first, "correction")
	wrong.EvidenceClass = "recorded_market_unqualified"
	if _, err := j.Retain(wrong); !errors.Is(err, ErrNewsLineage) {
		t.Fatal("cross-class parent accepted", err)
	}
	wrongHash := successor(Fixture(), first, "correction")
	wrongHash.Supersedes.RecordingSHA256 = strings.Repeat("a", 64)
	if _, err := j.Retain(wrongHash); !errors.Is(err, ErrNewsLineage) {
		t.Fatal("wrong predecessor digest accepted", err)
	}
	corrected := successor(Fixture(), first, "correction")
	second, err := j.Retain(corrected)
	if err != nil {
		t.Fatal(err)
	}
	retracted := successor(corrected, second, "retraction")
	third, err := j.Retain(retracted)
	if err != nil {
		t.Fatal(err)
	}
	revive := successor(retracted, third, "correction")
	revive.Claims[0].Assessment = "reported_unverified"
	if _, err := j.Retain(revive); !errors.Is(err, ErrNewsLineage) {
		t.Fatal("terminal retraction silently revived", err)
	}
	before, err := j.Status()
	if err != nil || len(before.Records) != 3 {
		t.Fatalf("status: %+v %v", before, err)
	}
	id := j.ID
	if err := j.Close(); err != nil {
		t.Fatal(err)
	}
	opened, err := OpenExistingJournal(path)
	if err != nil {
		t.Fatal(err)
	}
	defer opened.Close()
	after, err := opened.Status()
	if err != nil || opened.ID != id || !reflect.DeepEqual(before, after) {
		t.Fatal("reopen lost journal or lineage", err)
	}
	stored, err := opened.Inspect(first.Key)
	if err != nil || stored.Recording.Original != r.Original || stored.Result.RecordingSHA256 != first.RecordingSHA256 {
		t.Fatal("original or normalized source changed", err)
	}
	stored.Recording.Original = "mutated copy"
	fresh, _ := opened.Inspect(first.Key)
	if fresh.Recording.Original != r.Original {
		t.Fatal("inspection altered immutable source")
	}
}

func TestNewsJournalMissingBindingsAndRootGuards(t *testing.T) {
	for _, target := range []string{"manifest", "lock", "parent"} {
		t.Run(target, func(t *testing.T) {
			j, path := testJournal(t)
			first, err := j.Retain(Fixture())
			if err != nil {
				t.Fatal(err)
			}
			if target == "parent" {
				if _, err := j.Retain(successor(Fixture(), first, "correction")); err != nil {
					t.Fatal(err)
				}
			}
			file := filepath.Join(path, "journal.json")
			if target == "lock" {
				file = filepath.Join(path, "lock")
			}
			if target == "parent" {
				file = filepath.Join(path, "record-"+first.Key+".json")
			}
			if err := os.Remove(file); err != nil {
				t.Fatal(err)
			}
			if _, err := j.Status(); err == nil {
				t.Fatal("missing journal binding was ignored")
			}
		})
	}
	path := filepath.Join(t.TempDir(), "public")
	if err := os.Mkdir(path, 0755); err != nil {
		t.Fatal(err)
	}
	if j, err := OpenJournal(path); err == nil {
		_ = j.Close()
		t.Fatal("non-private source directory accepted")
	}
	target := filepath.Join(t.TempDir(), "actual")
	if err := os.Mkdir(target, 0700); err != nil {
		t.Fatal(err)
	}
	link := target + "-link"
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	if j, err := OpenJournal(link); err == nil {
		_ = j.Close()
		t.Fatal("symlink journal root accepted")
	}
}

func TestNewsJournalConcurrentOwnership(t *testing.T) {
	j, path := testJournal(t)
	if other, err := OpenExistingJournal(path); !errors.Is(err, ErrNewsBusy) {
		if other != nil {
			_ = other.Close()
		}
		t.Fatal("second owner accepted", err)
	}
	var wg sync.WaitGroup
	duplicates := make(chan bool, 8)
	for range 8 {
		wg.Go(func() {
			r, err := j.Retain(Fixture())
			if err != nil {
				t.Error(err)
			}
			duplicates <- r.Duplicate
		})
	}
	wg.Wait()
	close(duplicates)
	n := 0
	for duplicate := range duplicates {
		if duplicate {
			n++
		}
	}
	if n != 7 {
		t.Fatalf("concurrent retries created multiple records: %d duplicates", n)
	}
}

func TestNewsProcessLock(t *testing.T) {
	if path := os.Getenv("MANTIK_NEWS_LOCK_TEST"); path != "" {
		j, err := OpenExistingJournal(path)
		if j != nil {
			_ = j.Close()
		}
		if !errors.Is(err, ErrNewsBusy) {
			t.Fatal("process did not observe owner lock", err)
		}
		return
	}
	_, path := testJournal(t)
	cmd := exec.Command(os.Args[0], "-test.run=^TestNewsProcessLock$")
	cmd.Env = append(os.Environ(), "MANTIK_NEWS_LOCK_TEST="+path)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("cross-process lock: %v %s", err, out)
	}
}

func TestNewsJournalCorruptionAndTemporaryArtifacts(t *testing.T) {
	for _, kind := range []string{"source", "result", "manifest", "symlink", "permissions"} {
		t.Run(kind, func(t *testing.T) {
			j, path := testJournal(t)
			receipt, err := j.Retain(Fixture())
			if err != nil {
				t.Fatal(err)
			}
			file := filepath.Join(path, "record-"+receipt.Key+".json")
			stored, _ := j.Inspect(receipt.Key)
			_ = j.Close()
			switch kind {
			case "source":
				stored.Recording.Original += " changed"
				raw, _ := json.Marshal(stored)
				if err := os.WriteFile(file, raw, 0600); err != nil {
					t.Fatal(err)
				}
			case "result":
				stored.Result.Claims[0].Assertion = "changed derived cache"
				raw, _ := json.Marshal(stored)
				if err := os.WriteFile(file, raw, 0600); err != nil {
					t.Fatal(err)
				}
			case "manifest":
				raw, _ := json.Marshal(journalManifest{Schema: JournalSchema, ID: "news-" + strings.Repeat("A", 26)})
				if err := os.WriteFile(filepath.Join(path, "journal.json"), raw, 0600); err != nil {
					t.Fatal(err)
				}
			case "symlink":
				if err := os.Rename(file, file+".outside"); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(file+".outside", file); err != nil {
					t.Fatal(err)
				}
			case "permissions":
				if err := os.Chmod(file, 0644); err != nil {
					t.Fatal(err)
				}
			}
			if opened, err := OpenExistingJournal(path); err == nil {
				_ = opened.Close()
				t.Fatal("corrupt journal accepted")
			}
		})
	}
	j, path := testJournal(t)
	first, err := j.Retain(Fixture())
	if err != nil {
		t.Fatal(err)
	}
	temp := "tmp-" + strings.Repeat("A", 26)
	if err := os.WriteFile(filepath.Join(path, temp), []byte("partial source evidence"), 0600); err != nil {
		t.Fatal(err)
	}
	status, err := j.Status()
	if err != nil || len(status.Records) != 1 || !reflect.DeepEqual(status.PendingArtifacts, []string{temp}) {
		t.Fatal("temporary file became a published record", err)
	}
	retry, err := j.Retain(Fixture())
	if err != nil || !retry.Duplicate || retry.Key != first.Key {
		t.Fatal("temporary artifact broke exact published retry", err)
	}
	if _, err := os.Stat(filepath.Join(path, temp)); err != nil {
		t.Fatal("pending evidence was deleted")
	}
}

func TestNewsJournalCapacity(t *testing.T) {
	j, _ := testJournal(t)
	first := Fixture()
	for n := range MaxRecords {
		r := Fixture()
		r.ArticleID = fmt.Sprintf("article-%03d", n)
		if n == 0 {
			first = r
		}
		if _, err := j.Retain(r); err != nil {
			t.Fatal(n, err)
		}
	}
	if _, err := j.Retain(Fixture()); !errors.Is(err, ErrNewsCapacity) {
		t.Fatal("capacity exceeded", err)
	}
	if receipt, err := j.Retain(first); err != nil || !receipt.Duplicate {
		t.Fatal("capacity blocked exact retry", err)
	}
}
