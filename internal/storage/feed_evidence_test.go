package storage

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/feedcollector"
)

type evidenceBackend interface {
	Store
	FeedCollectorStore
	FeedEvidenceStore
}

func TestMemoryFeedEvidence(t *testing.T) { feedEvidenceContracts(t, NewMemory()) }
func TestPostgresContractsFeedEvidence(t *testing.T) {
	feedEvidenceContracts(t, timerPostgres(t, "feedevidence"))
}

func feedEvidenceContracts(t *testing.T, s evidenceBackend) {
	t.Helper()
	ctx := context.Background()
	at := time.Date(2026, 10, 15, 13, 30, 0, 0, time.UTC)
	now := func() time.Time { return at }
	initial, _ := s.FeedCollectors(ctx)
	receiver := initial.ReceiverID
	r := feedcollector.Registration{ID: "feed", Provider: "alpaca", Feed: "iex", Symbol: "SPY", EvidenceClass: "prospective_market_unqualified", JournalID: "journal-one", AccessRef: "access-one", LeaseSeconds: 300}
	if _, err := s.FeedCollectorCommand(ctx, receiver, feedcollector.Command{RequestID: "register", Operation: "register", RegistrationID: r.ID, Registration: &r}, now); err != nil {
		t.Fatal(err)
	}
	f := feedcollector.Fence{RegistrationID: r.ID, OwnerID: "owner", Generation: 1}
	raw := json.RawMessage(`{"original":"private synthetic bytes","revision":1}`)
	check := func(snapshot FeedEvidenceSnapshot, when time.Time, previous *FeedEvidenceRecord) error {
		if snapshot.ReceiverID != receiver || !when.Equal(at) {
			t.Error("incoherent validator snapshot")
		}
		return nil
	}
	if _, err := s.PublishFeedEvidence(ctx, "receiver-abcdefghijklmnop", r.ID, "capture-r1", f, raw, now, check); !errors.Is(err, ErrReceiverMismatch) {
		t.Fatal(err)
	}
	rejected := func(FeedEvidenceSnapshot, time.Time, *FeedEvidenceRecord) error { return ErrEvidenceInvalid }
	if _, err := s.PublishFeedEvidence(ctx, receiver, r.ID, "capture-r1", f, raw, now, rejected); !errors.Is(err, ErrEvidenceInvalid) {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	var first atomic.Int32
	var receipt FeedEvidenceReceipt
	var mu sync.Mutex
	for range 8 {
		wg.Go(func() {
			got, err := s.PublishFeedEvidence(ctx, receiver, r.ID, "capture-r1", f, raw, now, check)
			if err != nil {
				t.Error(err)
			} else if !got.Duplicate {
				first.Add(1)
				mu.Lock()
				receipt = got
				mu.Unlock()
			}
		})
	}
	wg.Wait()
	if first.Load() != 1 {
		t.Fatal("immutable publication multiplied", first.Load())
	}
	if _, err := s.PublishFeedEvidence(ctx, receiver, r.ID, "capture-r1", f, json.RawMessage(`{"different":true}`), now, check); !errors.Is(err, ErrEvidenceConflict) {
		t.Fatal("capture overwritten", err)
	}
	before, err := s.FeedEvidence(ctx)
	if err != nil || len(before.Records) != 1 {
		t.Fatal(err)
	}
	if pg, ok := s.(*Postgres); ok {
		config := pg.pool.Config().ConnString()
		pg.Close()
		reopened, err := OpenPostgres(ctx, config)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(reopened.Close)
		s = reopened
		retry, err := s.PublishFeedEvidence(ctx, receiver, r.ID, "capture-r1", f, raw, now, check)
		retry.Duplicate = false
		if err != nil || retry != receipt {
			t.Fatal("restart changed canonical receipt digest", retry, receipt, err)
		}
		baseline, _ := s.FeedEvidence(ctx)
		if _, err := reopened.pool.Exec(ctx, "ALTER TABLE mantik_feed_collectors ADD CONSTRAINT reject_publication_clock CHECK(payload->>'last_clock'<>'2026-10-15T13:30:01Z')"); err != nil {
			t.Fatal(err)
		}
		at = at.Add(time.Second)
		if _, err := s.PublishFeedEvidence(ctx, receiver, r.ID, "rollback-r1", f, raw, now, check); err == nil {
			t.Fatal("failed collector clock write admitted publication")
		}
		after, _ := s.FeedEvidence(ctx)
		if !reflect.DeepEqual(baseline, after) {
			t.Fatal("partial raw evidence commit")
		}
		if _, err := reopened.pool.Exec(ctx, "ALTER TABLE mantik_feed_collectors DROP CONSTRAINT reject_publication_clock"); err != nil {
			t.Fatal(err)
		}
		if _, err := reopened.pool.Exec(ctx, "UPDATE mantik_schema SET sha256='changed' WHERE version=10"); err != nil {
			t.Fatal(err)
		}
		if bad, err := OpenPostgres(ctx, config); err == nil {
			bad.Close()
			t.Fatal("changed evidence migration admitted")
		}
	}
}
