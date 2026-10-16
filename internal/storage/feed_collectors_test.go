package storage

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/feedcollector"
	"git.subcult.tv/subculture-collective/mantik/internal/workflow"
)

type collectorBackend interface {
	Store
	FeedCollectorStore
}

func TestMemoryFeedCollectors(t *testing.T) { collectorContracts(t, NewMemory()) }
func TestPostgresContractsFeedCollectors(t *testing.T) {
	collectorContracts(t, timerPostgres(t, "feedcollectors"))
}

func collectorContracts(t *testing.T, store collectorBackend) {
	t.Helper()
	ctx := context.Background()
	at := time.Date(2026, 10, 15, 13, 30, 0, 0, time.UTC)
	now := func() time.Time { return at }
	before, err := store.Snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := store.FeedCollectors(ctx)
	if err != nil {
		t.Fatal(err)
	}
	receiver := snapshot.ReceiverID
	r := feedcollector.Registration{ID: "spy-iex", Provider: "alpaca", Feed: "iex", Symbol: "SPY", EvidenceClass: "prospective_market_unqualified", JournalID: "journal-one", AccessRef: "access-one", LeaseSeconds: 30}
	register := feedcollector.Command{RequestID: "register-one", Operation: "register", RegistrationID: r.ID, Registration: &r}
	if _, err := store.FeedCollectorCommand(ctx, "receiver-abcdefghijklmnop", register, now); !errors.Is(err, ErrReceiverMismatch) {
		t.Fatal(err)
	}
	if _, err := store.FeedCollectorCommand(ctx, receiver, register, now); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	var successes atomic.Int32
	var mu sync.Mutex
	var winning feedcollector.Receipt
	for n := range 8 {
		wg.Go(func() {
			c := feedcollector.Command{RequestID: fmt.Sprintf("claim-%d", n), Operation: "claim", RegistrationID: r.ID, OwnerID: fmt.Sprintf("worker-%d", n)}
			receipt, err := store.FeedCollectorCommand(ctx, receiver, c, now)
			if err == nil {
				successes.Add(1)
				mu.Lock()
				winning = receipt
				mu.Unlock()
			} else if !errors.Is(err, feedcollector.ErrConflict) {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	if successes.Load() != 1 {
		t.Fatal("multiple owners", successes.Load())
	}
	claimed, err := store.FeedCollectors(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(claimed.State.Commands) != 2 {
		t.Fatal("rejected commands retained as success")
	}
	if pg, ok := store.(*Postgres); ok {
		config := pg.pool.Config().ConnString()
		pg.Close()
		reopened, err := OpenPostgres(ctx, config)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(reopened.Close)
		store = reopened
		after, err := store.FeedCollectors(ctx)
		if err != nil || !reflect.DeepEqual(claimed, after) {
			t.Fatal("restart lost ownership", err)
		}
	}
	claim := claimed.State.Commands[winning.RequestID].Command
	at = at.Add(time.Hour)
	for range 8 {
		wg.Go(func() {
			receipt, err := store.FeedCollectorCommand(ctx, receiver, claim, now)
			if err != nil || !reflect.DeepEqual(receipt, winning) {
				t.Error("retry changed lease", err)
			}
		})
	}
	wg.Wait()
	after, err := store.FeedCollectors(ctx)
	if err != nil || !reflect.DeepEqual(after, claimed) {
		t.Fatal("retries changed durable state", err)
	}
	takeover := feedcollector.Command{RequestID: "takeover", Operation: "claim", RegistrationID: r.ID, OwnerID: "replacement"}
	newOwner, err := store.FeedCollectorCommand(ctx, receiver, takeover, now)
	if err != nil || newOwner.Entry.Lease.Generation != 2 {
		t.Fatal("takeover failed", err)
	}
	stale := feedcollector.Command{RequestID: "stale-renew", Operation: "renew", RegistrationID: r.ID, OwnerID: winning.Entry.Lease.OwnerID, Generation: 1}
	if _, err := store.FeedCollectorCommand(ctx, receiver, stale, now); !errors.Is(err, feedcollector.ErrLeaseLost) {
		t.Fatal(err)
	}
	// Ordinary fixture aggregate mutations must leave collector history intact.
	unchanged, err := store.FeedCollectors(ctx)
	if err != nil {
		t.Fatal(err)
	}
	fixtureBefore, err := store.Snapshot(ctx)
	if err != nil || !reflect.DeepEqual(before, fixtureBefore) {
		t.Fatal("ownership changed research/trading state", err)
	}
	if _, err := store.Ingest(ctx, workflow.Fixtures()[1]); err != nil {
		t.Fatal(err)
	}
	retained, err := store.FeedCollectors(ctx)
	if err != nil || !reflect.DeepEqual(unchanged, retained) {
		t.Fatal("aggregate writer discarded ownership", err)
	}
	// Returned references cannot alter retained receipts or declarations.
	retained.State.Entries[r.ID].Lease.OwnerID = "tampered"
	winning.Entry.Lease.OwnerID = "tampered"
	actual, err := store.FeedCollectors(ctx)
	if err != nil || !reflect.DeepEqual(actual, unchanged) {
		t.Fatal("returned alias mutated store", err)
	}
	if pg, ok := store.(*Postgres); ok {
		// Failure at the sole persistence write rolls back lease and receipt.
		if _, err := pg.pool.Exec(ctx, "ALTER TABLE mantik_feed_collectors ADD CONSTRAINT reject_renew CHECK(NOT (payload->'commands' ? 'forced-failure'))"); err != nil {
			t.Fatal(err)
		}
		c := feedcollector.Command{RequestID: "forced-failure", Operation: "renew", RegistrationID: r.ID, OwnerID: "replacement", Generation: 2}
		if _, err := store.FeedCollectorCommand(ctx, receiver, c, now); err == nil {
			t.Fatal("forced persistence failure committed")
		}
		after, err := store.FeedCollectors(ctx)
		if err != nil || !reflect.DeepEqual(actual, after) {
			t.Fatal("partial ownership write", err)
		}
		if _, err := pg.pool.Exec(ctx, "ALTER TABLE mantik_feed_collectors DROP CONSTRAINT reject_renew"); err != nil {
			t.Fatal(err)
		}
		if _, err := store.FeedCollectorCommand(ctx, receiver, c, now); err != nil {
			t.Fatal(err)
		}
	}
}

func TestPostgresContractsFeedCollectorUpgrade(t *testing.T) {
	s := timerPostgres(t, "feedupgrade")
	ctx := context.Background()
	config := s.pool.Config().ConnString()
	before, err := s.Snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	receiver, err := s.ResearchSnapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.pool.Exec(ctx, "DROP TABLE mantik_external_trial_attempt_outputs; DROP TABLE mantik_external_trial_attempts; DROP TABLE mantik_external_trial_job_inputs; DROP TABLE mantik_external_trial_jobs; DROP TABLE mantik_trial_run_artifacts; DROP TABLE mantik_trial_runs; DROP TABLE mantik_trial_attempts; DROP TABLE mantik_hypothesis_trials; DROP TABLE mantik_hypothesis_publications; DROP TABLE mantik_news_reviews; DROP TABLE mantik_news_cases; DROP TABLE mantik_feed_evidence; DROP TABLE mantik_feed_collectors; DELETE FROM mantik_schema WHERE version>=8"); err != nil {
		t.Fatal(err)
	}
	s.Close()
	upgraded, err := OpenPostgres(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(upgraded.Close)
	feed, err := upgraded.FeedCollectors(ctx)
	if err != nil || feed.ReceiverID != receiver.ID || len(feed.State.Entries) != 0 || len(feed.State.Commands) != 0 {
		t.Fatal("upgrade invented owner/changed receiver", err)
	}
	after, err := upgraded.Snapshot(ctx)
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatal("upgrade changed fixture aggregate", err)
	}
	var maximum int
	if err := upgraded.pool.QueryRow(ctx, "SELECT max(version) FROM mantik_schema").Scan(&maximum); err != nil || maximum != 19 {
		t.Fatal(err)
	}
	if _, err := upgraded.pool.Exec(ctx, "UPDATE mantik_schema SET sha256='changed' WHERE version=8"); err != nil {
		t.Fatal(err)
	}
	if bad, err := OpenPostgres(ctx, config); err == nil {
		bad.Close()
		t.Fatal("migration tamper admitted")
	}
}
