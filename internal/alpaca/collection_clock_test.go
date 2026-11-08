package alpaca

import (
	"context"
	"fmt"
	"net/http"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

// steppingClock runs each read one second earlier than the last, as a clock
// being stepped backwards repeatedly would.
func steppingClock(start time.Time) func() time.Time {
	var reads atomic.Int64
	return func() time.Time { return start.Add(-time.Duration(reads.Add(1)) * time.Second) }
}

// A wall clock that steps backwards during a collection must not make the
// journal write records it then rejects as a conflict.
func TestCollectSurvivesBackwardClockSteps(t *testing.T) {
	j := testOpenJournal(t, filepath.Join(t.TempDir(), "journal"))
	j.clock = steppingClock(time.Now().UTC())
	c := testClient(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprint(w, actionPage("a", ""))
	})
	first, err := j.Collect(context.Background(), c, corporatePageSpec("first", ""))
	if err != nil || first.State != "retained" {
		t.Fatal(first, err)
	}
	if status, err := j.Collection("first"); err != nil || status.State != "retained" {
		t.Fatal("retained collection unreadable after clock step", status, err)
	}
	if got, err := j.CorporateActionPageChain("first", 1, time.Now().UTC()); err != nil || got.Status != "page_chain_terminated" {
		t.Fatal(got, err)
	}
}

// Stamps never run backwards and resume once the clock passes the latest one.
func TestJournalStampHoldsThroughBackwardStep(t *testing.T) {
	at := time.Date(2026, 11, 8, 6, 36, 0, 0, time.UTC)
	readings := []time.Time{at, at.Add(-3 * time.Second), at.Add(time.Second)}
	want := []time.Time{at, at, at.Add(time.Second)}
	j := &Journal{clock: func() time.Time { next := readings[0]; readings = readings[1:]; return next }}
	for i, expected := range want {
		if got := j.stamp(); !got.Equal(expected) {
			t.Fatal(i, got, expected)
		}
	}
}
