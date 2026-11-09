package service

import (
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/feedcollector"
	"git.subcult.tv/subculture-collective/mantik/internal/storage"
)

func retainedDecisions(t *testing.T, store *storage.Memory) []storage.PaperDecisionRecord {
	t.Helper()
	catalog, err := store.PaperDecisions(t.Context(), evaluateOwnedPaperDecision)
	if err != nil {
		t.Fatal(err)
	}
	return catalog.Records
}

// New quotes that only repeat an abstention retain nothing. A changed outcome
// is retained at once, and an entry candidate is never held back.
func TestAutomaticDecisionsRetainRepeatedAbstentionsOnce(t *testing.T) {
	f := newQuoteFixture(t)
	store := f.declared()
	clock := newFakeClock(f.at)
	step := func(id string, cents int, want int) {
		t.Helper()
		f.at = f.at.Add(20 * time.Second)
		clock.at = f.at
		f.quote(id, cents)
		// The fixture has no allocation policy, so comparing an entry
		// candidate fails after the decision itself is retained.
		if err := RunPaperDecisionCycle(t.Context(), store, clock); err != nil && !errors.Is(err, storage.ErrOwnedAllocationConflict) {
			t.Fatal(id, err)
		}
		if got := retainedDecisions(t, f.store); len(got) != want {
			t.Fatal(id, len(got), want)
		}
	}
	above := int(f.definition.EntryAtOrBelowCents) + 100
	step("first", above, 1)
	step("repeat-1", above+1, 1)
	step("repeat-2", above+2, 1)
	// At or below the threshold the worker retains an entry candidate.
	step("signal", int(f.definition.EntryAtOrBelowCents), 2)
}

func TestRepeatsAbstentionStopsAtTheInterval(t *testing.T) {
	f := newQuoteFixture(t)
	f.at = f.at.Add(20 * time.Second)
	f.quote("quote", int(f.definition.EntryAtOrBelowCents)+100)
	feed, err := f.store.FeedEvidence(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	c := storage.PaperDecisionCommand{ID: "c", ReceiverID: f.owner, StrategyID: f.definition.ID, StrategySHA256: f.strategy.Record.SHA256, RegistrationID: f.reg.ID}
	now, err := evaluateOwnedPaperDecision(c, storage.PaperDecisionBasis{Strategy: f.strategy.Record, Feed: feed}, f.at)
	if err != nil || now.Action != "abstain" {
		t.Fatal(now, err)
	}
	last := storage.PaperDecisionRecord{FinalizedAt: f.at.Add(-time.Minute), Evaluation: now}
	if !repeatsAbstention(last, c, f.strategy.Record, feed, f.at) {
		t.Fatal("recent identical abstention retained")
	}
	last.FinalizedAt = f.at.Add(-AutomaticAbstentionInterval)
	if repeatsAbstention(last, c, f.strategy.Record, feed, f.at) {
		t.Fatal("interval elapsed but abstention held back")
	}
	last.FinalizedAt, last.Evaluation.ReasonCode = f.at.Add(-time.Minute), "some_other_reason"
	if repeatsAbstention(last, c, f.strategy.Record, feed, f.at) {
		t.Fatal("changed reason held back")
	}
	last.Evaluation.Action = "entry_candidate"
	if repeatsAbstention(last, c, f.strategy.Record, feed, f.at) {
		t.Fatal("previous entry candidate suppressed the next decision")
	}
}

// TestPaperDecisionCycleAtCapacity measures a decision cycle at the raised
// limits. Set MANTIK_FEED_CAPACITY_BENCH=1 to run it.
func TestPaperDecisionCycleAtCapacity(t *testing.T) {
	if os.Getenv("MANTIK_FEED_CAPACITY_BENCH") != "1" {
		t.Skip("set MANTIK_FEED_CAPACITY_BENCH=1")
	}
	f := newQuoteFixture(t)
	const records, decisions = 2000, 250
	generation := uint64(1)
	last := f.at
	for n := range records {
		f.at = f.at.Add(time.Second)
		if f.at.Sub(last) > 200*time.Second {
			if _, err := f.store.FeedCollectorCommand(t.Context(), f.owner, feedcollector.Command{RequestID: fmt.Sprintf("renew-%d", n), Operation: "renew", RegistrationID: f.reg.ID, OwnerID: "worker", Generation: generation}, f.now); err != nil {
				t.Fatal(err)
			}
			last = f.at
		}
		if n%16 == 15 {
			f.publish(f.recording(fmt.Sprintf("calendar-%04d", n), "market_calendar", "regular_session", f.calendarRaw, f.date))
			continue
		}
		f.quote(fmt.Sprintf("quote-%04d", n), int(f.definition.EntryAtOrBelowCents)+100)
	}
	c := storage.PaperDecisionCommand{ReceiverID: f.owner, StrategyID: f.definition.ID, StrategySHA256: f.strategy.Record.SHA256, RegistrationID: f.reg.ID}
	var finalize time.Duration
	for n := range decisions {
		f.at = f.at.Add(time.Millisecond)
		c.ID = fmt.Sprintf("capacity-%04d", n)
		start := time.Now()
		if _, err := f.store.FinalizePaperDecision(t.Context(), c, f.now, evaluateOwnedPaperDecision); err != nil {
			t.Fatal(n, err)
		}
		finalize = time.Since(start)
	}
	start := time.Now()
	retained := retainedDecisions(t, f.store)
	read := time.Since(start)
	start = time.Now()
	clock := newFakeClock(f.at)
	if err := RunPaperDecisionCycle(t.Context(), f.declared(), clock); err != nil {
		t.Fatal(err)
	}
	t.Logf("records=%d decisions=%d last_finalize=%s catalog_read=%s cycle=%s", records, len(retained), finalize, read, time.Since(start))
}
