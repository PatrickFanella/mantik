package storage

import (
	"context"
	"errors"
	"os"
	"reflect"
	"sync"
	"testing"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/workflow"
)

func TestMemoryContracts(t *testing.T) { contracts(t, NewMemory()) }

func TestPostgresContracts(t *testing.T) {
	url := os.Getenv("MANTIK_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("use make test-integration for disposable PostgreSQL contracts")
	}
	ctx := context.Background()
	store, err := OpenPostgres(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	initial, err := store.Snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.pool.Exec(ctx, "ALTER TABLE mantik_events ADD CONSTRAINT test_entry_rollback CHECK (id <> 'demo-quote-2-decision')"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Ingest(ctx, workflow.Fixtures()[1]); err == nil {
		t.Fatal("forced entry event failure was accepted")
	}
	failedEntry, err := store.Snapshot(ctx)
	if err != nil || !reflect.DeepEqual(initial, failedEntry) {
		t.Fatalf("failed entry left partial decision or future conditions: %v", err)
	}
	if _, err := store.pool.Exec(ctx, "ALTER TABLE mantik_events DROP CONSTRAINT test_entry_rollback"); err != nil {
		t.Fatal(err)
	}
	contracts(t, store)
	routingContracts(t, store)
	before, err := store.Snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	store.Close()
	reopened, err := OpenPostgres(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	after, err := reopened.Snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if after.State.Case.Status != "closed" || after.ObservationCount != before.ObservationCount || len(after.Events) != len(before.Events) || !reflect.DeepEqual(after.State.Routing, before.State.Routing) {
		t.Fatal("restart lost state")
	}
	if _, err := reopened.pool.Exec(ctx, "ALTER TABLE mantik_events ADD CONSTRAINT test_rollback CHECK (id <> 'rollback-quote-observed')"); err != nil {
		t.Fatal(err)
	}
	o := workflow.Fixtures()[3]
	o.ID = "rollback-quote"
	o.OccurredAt = o.OccurredAt.Add(time.Minute)
	o.ReceivedAt = o.OccurredAt
	if _, err := reopened.Ingest(ctx, o); err == nil {
		t.Fatal("forced event failure was accepted")
	}
	rolledBack, err := reopened.Snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if rolledBack.ObservationCount != after.ObservationCount || len(rolledBack.Events) != len(after.Events) || !reflect.DeepEqual(rolledBack.State, after.State) {
		t.Fatal("failed transaction left partial observation/state")
	}
	if _, err := reopened.pool.Exec(ctx, "ALTER TABLE mantik_events DROP CONSTRAINT test_rollback"); err != nil {
		t.Fatal(err)
	}
	receipt, err := reopened.Ingest(ctx, workflow.Fixtures()[1])
	if err != nil || !receipt.Duplicate {
		t.Fatalf("restart retry: %+v %v", receipt, err)
	}
	caseContracts(t, reopened)
	activityContracts(t, reopened)
	if _, err := reopened.pool.Exec(ctx, "UPDATE mantik_schema SET sha256='changed' WHERE version=1"); err != nil {
		t.Fatal(err)
	}
	if changed, err := OpenPostgres(ctx, url); err == nil {
		changed.Close()
		t.Fatal("changed migration was accepted")
	}
}

func contracts(t *testing.T, store Store) {
	t.Helper()
	ctx := context.Background()
	quotes := workflow.Fixtures()
	invalid := quotes[0]
	invalid.Symbol = "SPY"
	if _, err := store.Ingest(ctx, invalid); err == nil {
		t.Fatal("real instrument accepted")
	}
	if _, err := store.Ingest(ctx, quotes[0]); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for range 12 {
		wg.Go(func() {
			if _, err := store.Ingest(ctx, quotes[1]); err != nil {
				t.Errorf("concurrent ingest: %v", err)
			}
		})
	}
	wg.Wait()
	drainFixtureActions(t, store, quotes[1].ReceivedAt)
	conflict := quotes[1]
	conflict.PriceCents++
	if _, err := store.Ingest(ctx, conflict); !errors.Is(err, ErrConflict) {
		t.Fatalf("expected conflict, got %v", err)
	}
	for _, o := range quotes[2:] {
		if _, err := store.Ingest(ctx, o); err != nil {
			t.Fatal(err)
		}
		drainFixtureActions(t, store, o.ReceivedAt)
	}
	snapshot, err := store.Snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	expectedEvents := 10
	if _, ok := store.(*Postgres); ok {
		expectedEvents = 12
	}
	if snapshot.ObservationCount != 4 || snapshot.State.Case.Status != "closed" || len(snapshot.Events) != expectedEvents {
		t.Fatalf("duplicate side effects: %+v", snapshot)
	}
	if snapshot.State.Case.Exit.RealizedPnLCents != -6000 {
		t.Fatal("incorrect realized outcome")
	}
	for _, o := range quotes {
		r, err := store.Ingest(ctx, o)
		if err != nil || !r.Duplicate {
			t.Fatalf("retry failed: %+v %v", r, err)
		}
	}
	after, err := store.Snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(after.Events) != len(snapshot.Events) {
		t.Fatal("retry appended events")
	}
	// Readbacks cannot mutate a store's internal state through pointers.
	snapshot.State.Case.Exit.PriceCents = 1
	again, err := store.Snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if again.State.Case.Exit.PriceCents != 9400 {
		t.Fatal("snapshot escaped internal state")
	}
}
