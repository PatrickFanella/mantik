package storage

import (
	"context"
	"reflect"
	"testing"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/feedcollector"
)

func TestMemoryCollectorTakeoverSerialization(t *testing.T) {
	collectorTakeoverSerialization(t, NewMemory())
}
func TestPostgresContractsCollectorTakeoverSerialization(t *testing.T) {
	collectorTakeoverSerialization(t, timerPostgres(t, "collectorrace"))
}

func collectorTakeoverSerialization(t *testing.T, s submissionBackend) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	at := time.Date(2026, 10, 15, 13, 30, 0, 0, time.UTC)
	snapshot, _ := s.FeedCollectors(ctx)
	receiver := snapshot.ReceiverID
	r := feedcollector.Registration{ID: "spy-iex", Provider: "alpaca", Feed: "iex", Symbol: "SPY", EvidenceClass: "prospective_market_unqualified", JournalID: "journal-one", AccessRef: "access-one", LeaseSeconds: 30}
	for _, c := range []feedcollector.Command{{RequestID: "register", Operation: "register", RegistrationID: r.ID, Registration: &r}, {RequestID: "claim", Operation: "claim", RegistrationID: r.ID, OwnerID: "worker-one"}} {
		if _, err := s.FeedCollectorCommand(ctx, receiver, c, func() time.Time { return at }); err != nil {
			t.Fatal(err)
		}
	}
	locked := make(chan struct{})
	release := make(chan struct{})
	submitted := make(chan error, 1)
	i := collectorInput(at, "locked-submit")
	go func() {
		_, err := s.SubmitCollectorResearch(ctx, receiver, feedcollector.Fence{RegistrationID: r.ID, OwnerID: "worker-one", Generation: 1}, i, func() time.Time {
			close(locked)
			select {
			case <-release:
			case <-ctx.Done():
			}
			return at
		})
		submitted <- err
	}()
	select {
	case <-locked:
	case <-ctx.Done():
		t.Fatal("submission did not acquire locks")
	}
	takingOver := make(chan struct{})
	takenOver := make(chan error, 1)
	go func() {
		close(takingOver)
		_, err := s.FeedCollectorCommand(ctx, receiver, feedcollector.Command{RequestID: "takeover", Operation: "claim", RegistrationID: r.ID, OwnerID: "worker-two"}, func() time.Time { return at.Add(30 * time.Second) })
		takenOver <- err
	}()
	<-takingOver
	close(release)
	if err := <-submitted; err != nil {
		t.Fatal(err)
	}
	if err := <-takenOver; err != nil {
		t.Fatal(err)
	}
	after, err := s.FeedCollectors(ctx)
	if err != nil || after.State.Entries[r.ID].Lease.Generation != 2 || after.State.Admissions[i.ID].Fence.Generation != 1 {
		t.Fatal("takeover raced admission", err)
	}
}

func TestPostgresContractsCollectorAdmissionUpgrade(t *testing.T) {
	s := timerPostgres(t, "admissionupgrade")
	ctx := context.Background()
	config := s.pool.Config().ConnString()
	at := time.Date(2026, 10, 15, 13, 30, 0, 0, time.UTC)
	initial, _ := s.FeedCollectors(ctx)
	r := feedcollector.Registration{ID: "spy-iex", Provider: "alpaca", Feed: "iex", Symbol: "SPY", EvidenceClass: "prospective_market_unqualified", JournalID: "journal-one", AccessRef: "access-one", LeaseSeconds: 30}
	for _, c := range []feedcollector.Command{{RequestID: "register", Operation: "register", RegistrationID: r.ID, Registration: &r}, {RequestID: "claim", Operation: "claim", RegistrationID: r.ID, OwnerID: "worker-one"}} {
		if _, err := s.FeedCollectorCommand(ctx, initial.ReceiverID, c, func() time.Time { return at }); err != nil {
			t.Fatal(err)
		}
	}
	before, _ := s.FeedCollectors(ctx)
	if _, err := s.pool.Exec(ctx, "ALTER TABLE mantik_feed_collectors DROP CONSTRAINT mantik_collectors_require_admissions; UPDATE mantik_feed_collectors SET payload=payload-'admissions'; DROP TABLE mantik_news_reviews; DROP TABLE mantik_news_cases; DROP TABLE mantik_feed_evidence; DELETE FROM mantik_schema WHERE version>=9"); err != nil {
		t.Fatal(err)
	}
	s.Close()
	upgraded, err := OpenPostgres(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(upgraded.Close)
	after, err := upgraded.FeedCollectors(ctx)
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatal("v8 upgrade changed ownership", err)
	}
	if _, err := upgraded.pool.Exec(ctx, "UPDATE mantik_feed_collectors SET payload=payload-'admissions'"); err == nil {
		t.Fatal("older lease writer discarded admissions")
	}
	if _, err := upgraded.pool.Exec(ctx, "UPDATE mantik_schema SET sha256='changed' WHERE version=9"); err != nil {
		t.Fatal(err)
	}
	if bad, err := OpenPostgres(ctx, config); err == nil {
		bad.Close()
		t.Fatal("admission checksum drift admitted")
	}
}
