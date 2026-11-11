package storage

import (
	"testing"

	"git.subcult.tv/subculture-collective/mantik/internal/workerplan"
)

// Archive N closes the owners active at epoch N-1. A record created at epoch 0
// and archived in archive 1 may leave archive 2 only when an approved
// retirement ends its interval at epoch 1.
func TestPaperArchiveOmitsOnlyRetiredOwners(t *testing.T) {
	pin := func(id string) ArchivedPaperRecord {
		return ArchivedPaperRecord{PaperArchiveRecordPin: PaperArchiveRecordPin{PaperRecordEpoch: PaperRecordEpoch{Kind: "decision", ID: id}, PayloadSHA256: workerplan.Hash(id)}}
	}
	first, kept := pin("first"), pin("kept")
	old := PaperRecordArchive{PaperRecordArchiveHeader: PaperRecordArchiveHeader{Sequence: 1, SHA256: "one"}, Records: []ArchivedPaperRecord{first, kept}}
	next := PaperRecordArchive{PaperRecordArchiveHeader: PaperRecordArchiveHeader{Sequence: 2, PreviousSHA256: "one"}, Records: []ArchivedPaperRecord{kept}}
	for _, c := range []struct {
		name    string
		retired map[string]int64
		ok      bool
	}{
		{"retired by the next epoch", map[string]int64{"decision:first": 1}, true},
		{"not retired", nil, false},
		{"retired after the next epoch", map[string]int64{"decision:first": 2}, false},
		{"another owner retired", map[string]int64{"decision:kept": 1}, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			if err := paperArchiveExtends(old, next.Records, c.retired); (err == nil) != c.ok {
				t.Fatal("writer extension", err)
			}
			original := map[string]originalPaperArchivePin{}
			if err := auditPaperArchiveStep(PaperRecordArchiveHeader{}, old, original, c.retired); err != nil {
				t.Fatal("first archive", err)
			}
			if err := auditPaperArchiveStep(old.PaperRecordArchiveHeader, next, original, c.retired); (err == nil) != c.ok {
				t.Fatal("audit step", err)
			}
		})
	}
	// A changed record still conflicts even when another owner retired.
	changed := pin("kept")
	changed.PayloadSHA256 = workerplan.Hash("changed")
	if err := paperArchiveExtends(old, []ArchivedPaperRecord{changed}, map[string]int64{"decision:first": 1}); err == nil {
		t.Fatal("writer accepted a changed record")
	}
}
