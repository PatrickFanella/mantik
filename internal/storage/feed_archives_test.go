package storage

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"sync"
	"testing"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/feedcollector"
)

type feedArchiveBackend interface {
	evidenceBackend
	FeedArchiveStore
}

func TestMemoryFeedArchives(t *testing.T) { feedArchiveContracts(t, NewMemory()) }
func TestPostgresContractsFeedArchives(t *testing.T) {
	feedArchiveContracts(t, timerPostgres(t, "feedarchives"))
}

func feedArchiveContracts(t *testing.T, s feedArchiveBackend) {
	t.Helper()
	ctx := t.Context()
	at := time.Date(2026, 11, 9, 15, 0, 0, 0, time.UTC)
	now := func() time.Time { return at }
	initial, err := s.FeedCollectors(ctx)
	if err != nil {
		t.Fatal(err)
	}
	owner := initial.ReceiverID
	registration := feedcollector.Registration{ID: "archive-feed", Provider: "alpaca", Feed: "iex", Symbol: "SPY", EvidenceClass: "prospective_market_unqualified", JournalID: "archive-journal", AccessRef: "manufactured", LeaseSeconds: 300}
	if _, err := s.FeedCollectorCommand(ctx, owner, feedcollector.Command{RequestID: "register", Operation: "register", RegistrationID: registration.ID, Registration: &registration}, now); err != nil {
		t.Fatal(err)
	}
	check := func(snapshot FeedEvidenceSnapshot, _ time.Time) error {
		if snapshot.ReceiverID != owner || len(snapshot.Collectors.Entries) != 1 {
			return ErrFeedArchiveInvalid
		}
		return nil
	}
	publish := func(n int) {
		t.Helper()
		at = at.Add(time.Second)
		f := feedcollector.Fence{RegistrationID: registration.ID, OwnerID: "worker", Generation: 1}
		if _, err := s.PublishFeedEvidence(ctx, owner, registration.ID, fmt.Sprintf("capture-%03d", n), f,
			json.RawMessage(fmt.Sprintf(`{"manufactured":%d}`, n)), now,
			func(FeedEvidenceSnapshot, time.Time, *FeedEvidenceRecord) error { return nil }); err != nil {
			t.Fatal(err)
		}
	}
	publish(0)
	before, err := s.FeedEvidence(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.ArchiveFeed(ctx, "receiver-aaaaaaaaaaaaaaaaaaaa", now, check); !errors.Is(err, ErrReceiverMismatch) {
		t.Fatal("wrong owner archived sources", err)
	}
	if _, err := s.ArchiveFeed(ctx, owner, now, func(FeedEvidenceSnapshot, time.Time) error { return ErrFeedArchiveConflict }); !errors.Is(err, ErrFeedArchiveConflict) {
		t.Fatal("failed source replay archived evidence", err)
	}
	var wg sync.WaitGroup
	var mu sync.Mutex
	first := 0
	var receipt FeedArchiveReceipt
	for range 6 {
		wg.Go(func() {
			r, err := s.ArchiveFeed(ctx, owner, now, check)
			if err != nil {
				t.Error(err)
				return
			}
			mu.Lock()
			defer mu.Unlock()
			if !r.Duplicate {
				first++
			}
			receipt = r
		})
	}
	wg.Wait()
	if first != 1 || receipt.Header.Sequence != 1 {
		t.Fatal("same source cut produced multiple archives", first, receipt)
	}
	// New source events cannot backfill the archive clock, including equality.
	for index, offset := range []time.Duration{0, -time.Second} {
		command := feedcollector.Command{RequestID: fmt.Sprintf("clock-rejected-%d", index), Operation: "claim", RegistrationID: registration.ID, OwnerID: "late-worker"}
		if _, err := s.FeedCollectorCommand(ctx, owner, command, func() time.Time { return at.Add(offset) }); !errors.Is(err, feedcollector.ErrClockBackwards) {
			t.Fatal("source write crossed archive clock floor", offset, err)
		}
	}
	a, err := s.FeedArchive(ctx, 1)
	if err != nil || a.Validate() != nil || a.Source.ReceiverID != owner || len(a.Source.Records) != 1 || len(a.Source.Collectors.Commands) != 1 {
		t.Fatal("archive lost owner evidence", a.FeedArchiveHeader, err)
	}
	canonical, err := canonicalArchiveSource(before)
	if err != nil || !reflect.DeepEqual(a.Source, canonical) {
		t.Fatal("archive did not capture the exact owner snapshot", err)
	}
	a.Source.Records = nil
	again, err := s.FeedArchive(ctx, 1)
	if err != nil || len(again.Source.Records) != 1 {
		t.Fatal("archive read escaped storage ownership", err)
	}
	retry, err := s.ArchiveFeed(ctx, owner, func() time.Time { t.Fatal("exact cut retry sampled clock"); return time.Time{} }, check)
	if err != nil || !retry.Duplicate || retry.Header != receipt.Header {
		t.Fatal("archive retry changed original receipt", retry, err)
	}
	after, err := s.FeedEvidence(ctx)
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatal("copy archive changed active sources", err)
	}
	publish(1)
	second, err := s.ArchiveFeed(ctx, owner, now, check)
	if err != nil || second.Header.Sequence != 2 || second.Header.PreviousSHA256 != receipt.Header.SHA256 {
		t.Fatal("archive chain did not extend", second, err)
	}
	page, err := s.FeedArchives(ctx, 0, 1)
	if err != nil || len(page.Records) != 1 || page.NextAfter != 1 {
		t.Fatal("first archive page", page, err)
	}
	last, err := s.FeedArchives(ctx, page.NextAfter, 1)
	if err != nil || len(last.Records) != 1 || last.Records[0] != second.Header || last.NextAfter != 0 {
		t.Fatal("second archive page", last, err)
	}
	if _, err := s.FeedArchive(ctx, 3); !errors.Is(err, ErrFeedArchiveNotFound) {
		t.Fatal("unknown archive accepted", err)
	}
	if _, err := s.FeedArchives(ctx, -1, 1); !errors.Is(err, ErrFeedArchiveInvalid) {
		t.Fatal("invalid cursor accepted", err)
	}
	if pg, ok := s.(*Postgres); ok {
		connection := pg.pool.Config().ConnString()
		pg.Close()
		reopened, err := OpenPostgres(ctx, connection)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(reopened.Close)
		s = reopened
		retained, err := s.FeedArchive(ctx, 1)
		if err != nil || retained.FeedArchiveHeader != receipt.Header || !reflect.DeepEqual(retained.Source, canonical) {
			t.Fatal("restart changed archive source or identity", err)
		}
		publish(2)
		if _, err := reopened.pool.Exec(ctx, "ALTER TABLE mantik_feed_archives ADD CONSTRAINT fail_archive CHECK (sequence < 3)"); err != nil {
			t.Fatal(err)
		}
		failed, err := s.ArchiveFeed(ctx, owner, now, check)
		if err == nil || failed != (FeedArchiveReceipt{}) {
			t.Fatal("failed archive insert returned a receipt", failed, err)
		}
		unchanged, err := s.FeedArchives(ctx, 0, 3)
		if err != nil || len(unchanged.Records) != 2 {
			t.Fatal("failed archive partially advanced history", err)
		}
		if _, err := reopened.pool.Exec(ctx, "ALTER TABLE mantik_feed_archives DROP CONSTRAINT fail_archive"); err != nil {
			t.Fatal(err)
		}
		if _, err := s.ArchiveFeed(ctx, owner, now, check); err != nil {
			t.Fatal("rolled-back archive could not be retried", err)
		}
		if _, err := reopened.pool.Exec(ctx, "UPDATE mantik_feed_archives SET payload=convert_to('{}','UTF8') WHERE sequence=1"); err != nil {
			t.Fatal(err)
		}
		if _, err := s.FeedArchive(ctx, 1); !errors.Is(err, ErrFeedArchiveConflict) {
			t.Fatal("corrupt archived source accepted", err)
		}
	} else if m, ok := s.(*Memory); ok {
		m.mu.Lock()
		delete(m.evidence, registration.ID+":capture-000")
		m.mu.Unlock()
		if _, err := s.ArchiveFeed(ctx, owner, now, check); !errors.Is(err, ErrFeedArchiveConflict) {
			t.Fatal("new archive concealed a missing original source", err)
		}
		m.mu.Lock()
		m.feedArchives[0].Source.Records = nil
		m.mu.Unlock()
		if _, err := s.FeedArchive(ctx, 1); !errors.Is(err, ErrFeedArchiveConflict) {
			t.Fatal("changed archived source accepted", err)
		}
	}
}

func TestFeedArchiveRejectsSourceOwnerSubstitutionAndCancellation(t *testing.T) {
	m := NewMemory()
	source := m.decisionFeedLocked()
	a, err := newFeedArchive(source, FeedArchiveHeader{}, time.Now(), func(FeedEvidenceSnapshot, time.Time) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	a.Source.ReceiverID = "receiver-aaaaaaaaaaaaaaaaaaaa"
	if a.Validate() == nil {
		t.Fatal("source owner substitution passed archive validation")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := m.ArchiveFeed(ctx, m.receiverID, time.Now, func(FeedEvidenceSnapshot, time.Time) error { return nil }); !errors.Is(err, context.Canceled) {
		t.Fatal("cancelled archive committed", err)
	}
}
