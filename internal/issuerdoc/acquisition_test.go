package issuerdoc

import (
	"context"
	"errors"
	"reflect"
	"testing"
)

func TestAcquisitionVerifiesWithoutTheJournal(t *testing.T) {
	raw := manufacturedWorkbook(t, [][]string{spdrRow("SPY", "78462F103", "09/18/2026", "09/18/2026", "10/30/2026", "1.888834", "", "")})
	_, url := newDocumentServer(t, raw)
	j, _ := testJournal(t, url)
	if _, err := j.Collect(context.Background(), testSpec("custody")); err != nil {
		t.Fatal(err)
	}
	status, a, original, err := j.Retained("custody")
	if err != nil {
		t.Fatal(err)
	}
	doc, err := a.Verify(original)
	if err != nil || !reflect.DeepEqual(&doc, status.Document) || a.JournalID != j.ID {
		t.Fatal("binding does not rederive the journal parse", err)
	}
	altered := append([]byte{}, original...)
	altered[len(altered)-1] ^= 1
	moved := a
	moved.CompletedAt = moved.CompletedAt.Add(1)
	moved.SHA256 = moved.seal()
	other := a
	other.Spec.CUSIP = "78462F104"
	other.SHA256 = other.seal()
	for name, tc := range map[string]struct {
		a   Acquisition
		raw []byte
	}{"altered bytes": {a, altered}, "moved clock": {moved, original}, "other spec": {other, original}} {
		if _, err := tc.a.Verify(tc.raw); !errors.Is(err, ErrJournalConflict) {
			t.Fatal(name, "accepted", err)
		}
	}
	_, url = newDocumentServer(t, []byte("not a workbook"))
	j2, _ := testJournal(t, url)
	if _, err := j2.Collect(context.Background(), testSpec("unparseable")); err != nil {
		t.Fatal(err)
	}
	_, a2, raw2, err := j2.Retained("unparseable")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a2.Verify(raw2); !errors.Is(err, ErrUnparseable) {
		t.Fatal("unparseable document not distinguished from tampering", err)
	}
}
