package storage

import (
	"context"
	"crypto/sha256"
	"encoding/json"
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
	"git.subcult.tv/subculture-collective/mantik/migrations"
	"github.com/jackc/pgx/v5"
)

func TestMemoryRoutingContracts(t *testing.T) { routingContracts(t, NewMemory()) }

func routingContracts(t *testing.T, store interface {
	Store
	RoutingStore
}) {
	t.Helper()
	ctx := context.Background()
	at := time.Date(2026, 1, 5, 15, 5, 0, 0, time.UTC)
	d := domain.TriggerDefinition{ID: "persisted-event-v1", Owner: "operator", CaseKey: "persistent-research", Behavior: "research", Condition: domain.Condition{Kind: "event", EventName: "signal"}, ValidFrom: at.Add(-time.Minute), ValidUntil: at.Add(time.Hour), RepeatPolicy: "reopen", Requirements: domain.SourceRequirements{Source: "fixture", Symbol: "DEMO", EvidenceClass: "synthetic_unqualified", MaxAgeSeconds: 60}}
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			if _, err := store.RegisterTrigger(ctx, d); err != nil {
				t.Errorf("registration: %v", err)
			}
		})
	}
	wg.Wait()
	i := domain.RoutingInput{ID: "persistent-input", Kind: "event", Source: "fixture", Symbol: "DEMO", EvidenceClass: "synthetic_unqualified", OccurredAt: at, ReceivedAt: at, EventName: "signal"}
	for range 8 {
		wg.Go(func() {
			if _, err := store.RouteInput(ctx, i); err != nil {
				t.Errorf("routing: %v", err)
			}
		})
	}
	wg.Wait()
	snapshot, err := store.Snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var caseID string
	for _, c := range snapshot.State.Routing.Cases {
		if c.CaseKey == d.CaseKey {
			caseID = c.ID
			if c.MatchCount != 1 {
				t.Fatal("concurrent delivery multiplied work")
			}
		}
	}
	if caseID == "" {
		t.Fatal("case missing")
	}
	if err := store.CloseRoutingCase(ctx, caseID, "wrong-owner", at); err == nil {
		t.Fatal("wrong owner closed case")
	}
	if err := store.CloseRoutingCase(ctx, caseID, "operator", at.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	i.ID, i.OccurredAt, i.ReceivedAt = "persistent-input-2", at.Add(2*time.Second), at.Add(2*time.Second)
	r, err := store.RouteInput(ctx, i)
	if err != nil || r.Matches[0].Transition != "reopened" {
		t.Fatalf("reopening: %+v %v", r, err)
	}
	if _, err := store.RegisterTrigger(ctx, d); err != nil {
		t.Fatal(err)
	}
	d.Condition.EventName = "changed"
	if _, err := store.RegisterTrigger(ctx, d); err != routing.ErrConflict {
		t.Fatal("definition conflict lost")
	}
	// Readbacks cannot mutate definitions or runtime state.
	snapshot.State.Routing.Definitions[0].Owner = "corrupted"
	again, err := store.Snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, definition := range again.State.Routing.Definitions {
		if definition.Owner == "corrupted" {
			t.Fatal("readback mutated routing state")
		}
	}
}

func TestPostgresContractsV1RoutingUpgrade(t *testing.T) {
	base := os.Getenv("MANTIK_TEST_DATABASE_URL")
	if base == "" {
		t.Skip("use make test-integration for disposable migration checks")
	}
	ctx := context.Background()
	connection, err := pgx.Connect(ctx, base)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = connection.Close(ctx) }()
	u, err := url.Parse(base)
	if err != nil {
		t.Fatal(err)
	}
	name := u.Path[1:] + "_upgrade"
	if _, err := connection.Exec(ctx, "CREATE DATABASE "+pgx.Identifier{name}.Sanitize()+" TEMPLATE template0"); err != nil {
		t.Fatal(err)
	}
	defer func() { _, _ = connection.Exec(ctx, "DROP DATABASE "+pgx.Identifier{name}.Sanitize()) }()
	u.Path = "/" + name
	u.RawPath = ""
	legacy, err := pgx.Connect(ctx, u.String())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := legacy.Exec(ctx, migrations.Bootstrap); err != nil {
		t.Fatal(err)
	}
	if _, err := legacy.Exec(ctx, "CREATE TABLE mantik_schema(version integer PRIMARY KEY,sha256 text NOT NULL)"); err != nil {
		t.Fatal(err)
	}
	state, events, err := workflow.Apply(workflow.InitialState(), workflow.Fixtures()[1])
	if err != nil {
		t.Fatal(err)
	}
	state.Routing = nil
	stateRaw, err := json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	var oldPayload map[string]json.RawMessage
	if err := json.Unmarshal(stateRaw, &oldPayload); err != nil {
		t.Fatal(err)
	}
	delete(oldPayload, "routing")
	stateRaw, err = json.Marshal(oldPayload)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := legacy.Exec(ctx, "INSERT INTO mantik_state VALUES('demo',$1)", stateRaw); err != nil {
		t.Fatal(err)
	}
	quote := workflow.Fixtures()[1]
	quoteRaw, err := json.Marshal(quote)
	if err != nil {
		t.Fatal(err)
	}
	for index := range events {
		if events[index].Decision != nil {
			events[index].Decision.FutureConditions = nil
		}
	}
	receiptRaw, err := json.Marshal(domain.Receipt{ObservationID: quote.ID, Events: events})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := legacy.Exec(ctx, "INSERT INTO mantik_observations VALUES($1,$2,$3)", quote.ID, quoteRaw, receiptRaw); err != nil {
		t.Fatal(err)
	}
	for _, event := range events {
		raw, err := json.Marshal(event)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := legacy.Exec(ctx, "INSERT INTO mantik_events(id,observation_id,payload) VALUES($1,$2,$3)", event.ID, quote.ID, raw); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := legacy.Exec(ctx, "INSERT INTO mantik_schema VALUES(1,$1)", fmt.Sprintf("%x", sha256.Sum256([]byte(migrations.Bootstrap)))); err != nil {
		t.Fatal(err)
	}
	if err := legacy.Close(ctx); err != nil {
		t.Fatal(err)
	}
	upgraded, err := OpenPostgres(ctx, u.String())
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := upgraded.Snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.State.Case.Status != "open" || len(snapshot.State.Routing.Definitions) != 2 || snapshot.State.Case.Entry.PriceCents != 10000 || snapshot.ObservationCount != 1 {
		t.Fatal("upgrade lost original position or protection")
	}
	for _, event := range snapshot.Events {
		if event.Decision != nil && len(event.Decision.FutureConditions) != 0 {
			t.Fatal("upgrade rewrote original decision history")
		}
	}
	if _, err := upgraded.pool.Exec(ctx, "UPDATE mantik_state SET payload=payload-'routing' WHERE id='demo'"); err == nil {
		t.Fatal("older writer could silently discard routing payload")
	}
	routingContracts(t, upgraded)
	before, err := upgraded.Snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	upgraded.Close()
	reopened, err := OpenPostgres(ctx, u.String())
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	after, err := reopened.Snapshot(ctx)
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatal("restart changed routing state")
	}
	if _, err := reopened.pool.Exec(ctx, "UPDATE mantik_schema SET sha256='changed' WHERE version=2"); err != nil {
		t.Fatal(err)
	}
	if bad, err := OpenPostgres(ctx, u.String()); err == nil {
		bad.Close()
		t.Fatal("routing migration checksum change admitted")
	}
}
