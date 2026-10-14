package storage

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"reflect"
	"sync"
	"testing"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/domain"
	"git.subcult.tv/subculture-collective/mantik/internal/routing"
	"git.subcult.tv/subculture-collective/mantik/internal/workflow"
	"github.com/jackc/pgx/v5"
)

type timerBackend interface {
	Store
	RoutingStore
	TimerStore
}

func TestMemoryTimers(t *testing.T) {
	timerContracts(t, func(t *testing.T, name string) timerBackend { return NewMemory() })
}

// Each scenario creates a database only inside the disposable harness container.
func timerPostgres(t *testing.T, name string) *Postgres {
	t.Helper()
	base := os.Getenv("MANTIK_TEST_DATABASE_URL")
	if base == "" {
		t.Skip("use make test-integration")
	}
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, base)
	if err != nil {
		t.Fatal(err)
	}
	u, err := url.Parse(base)
	if err != nil {
		t.Fatal(err)
	}
	db := u.Path[1:] + "_" + name
	if _, err := conn.Exec(ctx, "CREATE DATABASE "+pgx.Identifier{db}.Sanitize()+" TEMPLATE template0"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, err := conn.Exec(ctx, "DROP DATABASE "+pgx.Identifier{db}.Sanitize())
		if err != nil {
			t.Error(err)
		}
		if err := conn.Close(ctx); err != nil {
			t.Error(err)
		}
	})
	u.Path = "/" + db
	u.RawPath = ""
	store, err := OpenPostgres(ctx, u.String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(store.Close)
	return store
}

func TestPostgresContractsTimers(t *testing.T) {
	timerContracts(t, func(t *testing.T, name string) timerBackend { return timerPostgres(t, name) })
}

func timerContracts(t *testing.T, factory func(*testing.T, string) timerBackend) {
	t.Helper()
	t.Run("protection", func(t *testing.T) {
		store := factory(t, "protection")
		ctx := context.Background()
		if _, err := store.Ingest(ctx, workflow.Fixtures()[1]); err != nil {
			t.Fatal(err)
		}
		drainFixtureActions(t, store, workflow.Fixtures()[1].ReceivedAt)
		snapshot, err := store.Snapshot(ctx)
		if err != nil {
			t.Fatal(err)
		}
		due := snapshot.State.Case.ExitBy
		// Eight independent workers contend on one persisted obligation.
		var wg sync.WaitGroup
		var mu sync.Mutex
		all := []domain.TimerClaim{}
		for n := range 8 {
			wg.Go(func() {
				claims, err := store.ClaimTimers(ctx, fmt.Sprintf("worker-%d", n), due, 30*time.Second)
				if err != nil {
					t.Error(err)
				}
				mu.Lock()
				all = append(all, claims...)
				mu.Unlock()
			})
		}
		wg.Wait()
		if len(all) != 1 {
			t.Fatalf("claims multiplied: %+v", all)
		}
		claim := all[0]
		receipt, err := store.CompleteTimer(ctx, claim, due)
		if err != nil || receipt.Status != "exception" {
			t.Fatalf("missing evidence: %+v %v", receipt, err)
		}
		for range 8 {
			wg.Go(func() {
				retry, err := store.CompleteTimer(ctx, claim, due)
				if err != nil || !reflect.DeepEqual(retry, receipt) {
					t.Errorf("concurrent completion changed effect: %v", err)
				}
			})
		}
		wg.Wait()
		after, err := store.Snapshot(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if len(after.Events) != len(snapshot.Events)+2 {
			t.Fatal("concurrent completion multiplied audit events")
		}
		if after.State.Case.Exit != nil || after.ObservationCount != 1 || len(after.State.Routing.Timers.Exceptions) != 1 {
			t.Fatal("timer fabricated fill or input")
		}
		beforeRetry := after
		retry, err := store.CompleteTimer(ctx, claim, due.Add(time.Hour))
		if err != nil || !reflect.DeepEqual(retry, receipt) {
			t.Fatal("completion retry changed result", err)
		}
		after, err = store.Snapshot(ctx)
		if err != nil || !reflect.DeepEqual(beforeRetry, after) {
			t.Fatal("retry multiplied effects")
		}
		oldQuote := workflow.Fixtures()[3]
		oldQuote.ID = "historical-after-timer"
		if _, err := store.Ingest(ctx, oldQuote); err != nil {
			t.Fatal(err)
		}
		after, err = store.Snapshot(ctx)
		if err != nil || after.State.Case.Exit != nil || !after.State.LastOccurredAt.Equal(workflow.Fixtures()[1].OccurredAt) {
			t.Fatal("historical input undid timer protection", err)
		}
		if pg, ok := store.(*Postgres); ok {
			pg.Close()
			reopened, err := OpenPostgres(ctx, pg.pool.Config().ConnString())
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(reopened.Close)
			store = reopened
			restarted, err := store.Snapshot(ctx)
			if err != nil || !reflect.DeepEqual(after, restarted) {
				t.Fatal("restart lost exception or receipt")
			}
		}
		// A real usable quote later resolves protection; the exception is preserved.
		q := workflow.Fixtures()[2]
		q.ID = "after-deadline"
		q.OccurredAt = due.Add(time.Second)
		q.ReceivedAt = q.OccurredAt
		if _, err := store.Ingest(ctx, q); err != nil {
			t.Fatal(err)
		}
		drainFixtureActions(t, store, q.ReceivedAt)
		after, err = store.Snapshot(ctx)
		if err != nil || after.State.Case.Exit == nil {
			t.Fatal("late quote failed safe exit", err)
		}
		for _, e := range after.State.Routing.Timers.Exceptions {
			if e.Status != "resolved" || !e.ResolvedAt.Equal(q.ReceivedAt) {
				t.Fatal("exception not reconciled")
			}
		}
	})
	t.Run("retained_price", func(t *testing.T) {
		store := factory(t, "price")
		ctx := context.Background()
		if _, err := store.Ingest(ctx, workflow.Fixtures()[1]); err != nil {
			t.Fatal(err)
		}
		drainFixtureActions(t, store, workflow.Fixtures()[1].ReceivedAt)
		snapshot, err := store.Snapshot(ctx)
		if err != nil {
			t.Fatal(err)
		}
		due := snapshot.State.Case.ExitBy
		quote := workflow.Fixtures()[2]
		quote.ID = "before-deadline"
		quote.OccurredAt = due.Add(-30 * time.Second)
		quote.ReceivedAt = quote.OccurredAt
		if _, err := store.Ingest(ctx, quote); err != nil {
			t.Fatal(err)
		}
		claims, err := store.ClaimTimers(ctx, "worker", due, 30*time.Second)
		if err != nil || len(claims) != 1 {
			t.Fatal("no deadline claim", err)
		}
		receipt, err := store.CompleteTimer(ctx, claims[0], due)
		if err != nil || receipt.Status != "executed" && receipt.Status != "queued" {
			t.Fatalf("retained-price execution: %+v %v", receipt, err)
		}
		drainFixtureActions(t, store, due)
		snapshot, err = store.Snapshot(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if snapshot.ObservationCount != 2 || snapshot.State.Case.Exit.PriceCents != quote.PriceCents || !snapshot.State.Case.Exit.ExecutedAt.Equal(due) || !snapshot.State.LastQuote.ReceivedAt.Equal(quote.ReceivedAt) {
			t.Fatal("timer changed evidence or fill")
		}
		for _, event := range receipt.Events {
			if event.Decision != nil && event.Decision.ObservationID != quote.ID {
				t.Fatal("decision lost source quote")
			}
		}
	})
	t.Run("recovery", func(t *testing.T) {
		store := factory(t, "recovery")
		ctx := context.Background()
		at := workflow.Fixtures()[0].ReceivedAt
		d := domain.TriggerDefinition{ID: "missing-event", Owner: "operator", CaseKey: "review", Behavior: "research", Condition: domain.Condition{Kind: "expected_event_missing", EventName: "done", DueAt: at.Add(time.Minute)}, ValidFrom: at, ValidUntil: at.Add(time.Hour), RepeatPolicy: "once", Requirements: domain.SourceRequirements{Source: "fixture", Symbol: "DEMO", EvidenceClass: "synthetic_unqualified", MaxAgeSeconds: 60}}
		if _, err := store.RegisterTrigger(ctx, d); err != nil {
			t.Fatal(err)
		}
		claims, err := store.ClaimTimers(ctx, "crashed", d.Condition.DueAt, 30*time.Second)
		if err != nil || len(claims) != 1 {
			t.Fatal(err)
		}
		old := claims[0]
		if pg, ok := store.(*Postgres); ok {
			config := pg.pool.Config().ConnString()
			pg.Close()
			next, err := OpenPostgres(ctx, config)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(next.Close)
			store = next
		}
		overdue := d.ValidUntil.Add(time.Hour)
		claims, err = store.ClaimTimers(ctx, "recovered", overdue, 30*time.Second)
		if err != nil || len(claims) != 1 || claims[0].Generation <= old.Generation {
			t.Fatal("overdue restart missed job", err)
		}
		if _, err := store.CompleteTimer(ctx, old, overdue); !errors.Is(err, routing.ErrLeaseLost) {
			t.Fatal("crashed worker could apply effect")
		}
		receipt, err := store.CompleteTimer(ctx, claims[0], overdue)
		if err != nil || receipt.Status != "matched" || len(receipt.Routing.Matches) != 1 {
			t.Fatal("missing event not routed", err)
		}
		if _, err := store.ClaimTimers(ctx, "rewound", at, time.Second); !errors.Is(err, routing.ErrClockBackwards) {
			t.Fatal("clock rewind not fenced")
		}
	})
}

func TestPostgresContractsTimerRollback(t *testing.T) {
	store := timerPostgres(t, "timerrollback")
	ctx := context.Background()
	if _, err := store.Ingest(ctx, workflow.Fixtures()[1]); err != nil {
		t.Fatal(err)
	}
	drainFixtureActions(t, store, workflow.Fixtures()[1].ReceivedAt)
	before, err := store.Snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	now := before.State.Case.ExitBy
	claims, err := store.ClaimTimers(ctx, "worker", now, 30*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	before, err = store.Snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.pool.Exec(ctx, "ALTER TABLE mantik_events ADD CONSTRAINT fail_timer CHECK (payload->>'kind' <> 'timer.completed')"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CompleteTimer(ctx, claims[0], now); err == nil {
		t.Fatal("forced audit failure accepted")
	}
	after, err := store.Snapshot(ctx)
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatal("partial timer/exception commit")
	}
	if _, err := store.pool.Exec(ctx, "ALTER TABLE mantik_events DROP CONSTRAINT fail_timer"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CompleteTimer(ctx, claims[0], now); err != nil {
		t.Fatal(err)
	}
	if _, err := store.pool.Exec(ctx, "UPDATE mantik_state SET payload=jsonb_set(payload,'{routing}',(payload->'routing')-'timers')"); err == nil {
		t.Fatal("old routing writer could drop timer leases")
	}
}

func TestPostgresContractsV2TimerUpgrade(t *testing.T) {
	store := timerPostgres(t, "v2timers")
	ctx := context.Background()
	if _, err := store.Ingest(ctx, workflow.Fixtures()[1]); err != nil {
		t.Fatal(err)
	}
	drainFixtureActions(t, store, workflow.Fixtures()[1].ReceivedAt)
	snapshot, err := store.Snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	// Construct the exact v2 shapes in a disposable database: two finalized
	// conditions, no timer state, no retained quote, original v1/v2 checksums.
	state := snapshot.State
	state.Routing.Definitions = state.Routing.Definitions[:2]
	for id := range state.Routing.Runtime {
		if _, exists := state.Routing.Timers.Jobs[id]; exists {
			delete(state.Routing.Runtime, id)
		}
	}
	state.Actions = nil
	state.Routing.Timers = nil
	state.LastQuote = nil
	raw, err := json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.pool.Exec(ctx, "ALTER TABLE mantik_state DROP CONSTRAINT mantik_state_requires_actions; DROP TABLE mantik_fixture_effects; ALTER TABLE mantik_state DROP CONSTRAINT mantik_state_requires_timers; ALTER TABLE mantik_events DROP CONSTRAINT mantik_event_requires_source; UPDATE mantik_events SET observation_id='demo-quote-2' WHERE observation_id IS NULL; ALTER TABLE mantik_events ALTER COLUMN observation_id SET NOT NULL; DELETE FROM mantik_schema WHERE version>=3"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.pool.Exec(ctx, "UPDATE mantik_state SET payload=($1::jsonb #- '{routing,timers}')-'actions'", raw); err != nil {
		t.Fatal(err)
	}
	legacyEvents := []domain.Event{}
	for _, event := range snapshot.Events {
		if event.Kind != "action.submitting" {
			event.ActionID = ""
			legacyEvents = append(legacyEvents, event)
		}
	}
	snapshot.Events = legacyEvents
	if _, err := store.pool.Exec(ctx, "DELETE FROM mantik_events WHERE payload->>'kind'='action.submitting'"); err != nil {
		t.Fatal(err)
	}
	for index := range snapshot.Events {
		event := &snapshot.Events[index]
		if event.Decision != nil {
			event.Decision.FutureConditions = event.Decision.FutureConditions[:2]
		}
		raw, err := json.Marshal(event)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := store.pool.Exec(ctx, "UPDATE mantik_events SET payload=$1 WHERE id=$2", raw, event.ID); err != nil {
			t.Fatal(err)
		}
	}
	receipt := domain.Receipt{ObservationID: workflow.Fixtures()[1].ID, Events: snapshot.Events}
	raw, err = json.Marshal(receipt)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.pool.Exec(ctx, "UPDATE mantik_observations SET receipt=$1 WHERE id=$2", raw, receipt.ObservationID); err != nil {
		t.Fatal(err)
	}
	config := store.pool.Config().ConnString()
	store.Close()
	legacy, err := pgx.Connect(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	entryID := workflow.Fixtures()[1].ID + "-decision"
	var entryRaw []byte
	if err := legacy.QueryRow(ctx, "SELECT payload FROM mantik_events WHERE id=$1", entryID).Scan(&entryRaw); err != nil {
		t.Fatal(err)
	}
	if _, err := legacy.Exec(ctx, "DELETE FROM mantik_events WHERE id=$1", entryID); err != nil {
		t.Fatal(err)
	}
	if incomplete, err := OpenPostgres(ctx, config); err == nil {
		incomplete.Close()
		t.Fatal("missing finalized authority admitted")
	}
	var version int
	if err := legacy.QueryRow(ctx, "SELECT max(version) FROM mantik_schema").Scan(&version); err != nil || version != 2 {
		t.Fatal("failed upgrade partially changed schema", err)
	}
	if _, err := legacy.Exec(ctx, "INSERT INTO mantik_events(id,observation_id,payload) VALUES($1,$2,$3)", entryID, receipt.ObservationID, entryRaw); err != nil {
		t.Fatal(err)
	}
	if err := legacy.Close(ctx); err != nil {
		t.Fatal(err)
	}
	upgraded, err := OpenPostgres(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(upgraded.Close)
	after, err := upgraded.Snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	// Restoring the original event retains its bytes but gives it a new sequence;
	// compare identity/content rather than the intentionally changed SQL order.
	byID := map[string]domain.Event{}
	for _, event := range after.Events {
		byID[event.ID] = event
	}
	preserved := len(after.Events) == len(snapshot.Events)
	for _, event := range snapshot.Events {
		preserved = preserved && reflect.DeepEqual(event, byID[event.ID])
	}
	if len(after.State.Routing.Definitions) != 3 || len(after.State.Routing.Timers.Jobs) != 1 || after.State.LastQuote == nil || !preserved {
		t.Fatal("v2 upgrade lost proof or deadline")
	}
	retry, err := upgraded.Ingest(ctx, workflow.Fixtures()[1])
	if err != nil || !retry.Duplicate || !reflect.DeepEqual(retry.Events, receipt.Events) {
		t.Fatal("upgrade rewrote retained receipt", err)
	}
	claims, err := upgraded.ClaimTimers(ctx, "recovered", after.State.Case.ExitBy.Add(time.Hour), 30*time.Second)
	if err != nil || len(claims) != 1 {
		t.Fatal("upgraded deadline not recovered", err)
	}
	result, err := upgraded.CompleteTimer(ctx, claims[0], after.State.Case.ExitBy.Add(time.Hour))
	if err != nil || result.Status != "exception" {
		t.Fatal("upgraded position fabricated overdue fill", err)
	}
	if _, err := upgraded.pool.Exec(ctx, "UPDATE mantik_schema SET sha256='changed' WHERE version=3"); err != nil {
		t.Fatal(err)
	}
	if changed, err := OpenPostgres(ctx, config); err == nil {
		changed.Close()
		t.Fatal("timer checksum tamper accepted")
	}
}
