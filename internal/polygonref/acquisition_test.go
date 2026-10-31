package polygonref

import (
	"bytes"
	"context"
	"errors"
	"reflect"
	"testing"
	"time"
)

func TestAcquisitionVerifiesWithoutTheJournal(t *testing.T) {
	s := dividendSpec("custody")
	next := "https://api.polygon.io/v3/reference/dividends?cursor=page2"
	_, origin := newFakePolygon(t, map[string][]byte{
		firstQuery(s):                          pageBody(next, dividendRecord("Ea1", "2026-03-20", "1.796999")),
		"/v3/reference/dividends?cursor=page2": pageBody("", dividendRecord("Eb2", "2026-09-18", "1.888834")),
	})
	j, _, _ := testJournal(t, origin)
	if _, err := j.Collect(context.Background(), s, testKey); err != nil {
		t.Fatal(err)
	}
	status, a, bodies, err := j.Retained(s.ID)
	if err != nil {
		t.Fatal(err)
	}
	snap, err := a.Verify(bodies)
	if err != nil || !reflect.DeepEqual(&snap, status.Snapshot) || a.JournalID != j.ID || len(a.Pages) != 2 {
		t.Fatal("binding does not rederive the journal snapshot", err)
	}
	altered := bytes.Replace(bodies[1], []byte("1.888834"), []byte("1.888835"), 1)
	for name, tc := range map[string]struct {
		a      Acquisition
		bodies [][]byte
	}{
		"altered body":    {a, [][]byte{bodies[0], altered}},
		"dropped page":    {a, bodies[:1]},
		"reordered pages": {a, [][]byte{bodies[1], bodies[0]}},
		"moved clock": {func() Acquisition {
			b := a
			b.CompletedAt = b.CompletedAt.Add(time.Second)
			b.SHA256 = b.seal()
			return b
		}(), bodies},
		"other journal":   {func() Acquisition { b := a; b.JournalID = "polygon-journal-other"; b.SHA256 = b.seal(); return b }(), bodies},
		"truncated chain": {func() Acquisition { b := a; b.Pages = b.Pages[:1]; b.SHA256 = b.seal(); return b }(), bodies[:1]},
	} {
		if _, err := tc.a.Verify(tc.bodies); !errors.Is(err, ErrJournalConflict) {
			t.Fatal(name, "accepted", err)
		}
	}
	failed := dividendSpec("failed")
	fake, origin := newFakePolygon(t, nil)
	fake.status.Store(429)
	j2, _, _ := testJournal(t, origin)
	_, _ = j2.Collect(context.Background(), failed, testKey)
	if _, _, _, err := j2.Retained(failed.ID); !errors.Is(err, ErrCollectionUnresolved) {
		t.Fatal("failed collection bound for custody", err)
	}
}
