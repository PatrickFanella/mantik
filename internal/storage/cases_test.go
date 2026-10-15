package storage

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/domain"
	"git.subcult.tv/subculture-collective/mantik/internal/workflow"
)

func TestMemoryCases(t *testing.T) {
	m := NewMemory()
	caseContracts(t, m)
	ctx := context.Background()
	catalog, _ := m.Cases(ctx)
	catalog.Fixture.ID = "changed"
	catalog.Routing[0].ID = "changed"
	again, _ := m.Cases(ctx)
	if again.Fixture.ID == "changed" || again.Routing[0].ID == "changed" {
		t.Fatal("catalogue aliases storage")
	}
	detail, _ := m.InspectCase(ctx, again.Routing[0].ID)
	detail.LastInput.Input.ID = "changed"
	detail, _ = m.InspectCase(ctx, again.Routing[0].ID)
	if detail.LastInput.Input.ID == "changed" {
		t.Fatal("inspection aliases input")
	}
	m.mu.Lock()
	delete(m.state.Routing.Inputs, strings.TrimPrefix(detail.Routing.LastInputRef, "routing:"))
	m.mu.Unlock()
	detail, err := m.InspectCase(ctx, again.Routing[0].ID)
	if err != nil || detail.ReferencesAvailable || detail.LastInput != nil {
		t.Fatal("missing evidence reported available")
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := m.Cases(cancelled); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if _, err := m.InspectCase(cancelled, again.Fixture.ID); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if _, err := caseCatalog(domain.DecisionCase{}, make([]domain.RoutingCase, 260)); err != nil {
		t.Fatal(err)
	}
	if _, err := caseCatalog(domain.DecisionCase{}, make([]domain.RoutingCase, 261)); !errors.Is(err, errCaseCapacity) {
		t.Fatal("over-capacity readback accepted")
	}
}

func TestMemoryCaseSources(t *testing.T) { caseSourceContracts(t, NewMemory()) }
func TestPostgresContractsCaseSources(t *testing.T) {
	caseSourceContracts(t, timerPostgres(t, "case_sources"))
}

func caseSourceContracts(t *testing.T, s interface {
	Store
	RoutingStore
	TimerStore
	CaseReader
}) {
	t.Helper()
	ctx := context.Background()
	o := workflow.Fixtures()[0]
	at := o.ReceivedAt
	d := domain.TriggerDefinition{ID: "case-source-quote", Owner: "operator", CaseKey: "case-source-quote", Behavior: "research", Condition: domain.Condition{Kind: "threshold", Comparison: "at_or_below", PriceCents: 20000}, ValidFrom: at.Add(-time.Minute), ValidUntil: at.Add(time.Hour), RepeatPolicy: "once", Requirements: domain.SourceRequirements{Source: "fixture", Symbol: "DEMO", EvidenceClass: "synthetic_unqualified", MaxAgeSeconds: 60}}
	if _, err := s.RegisterTrigger(ctx, d); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Ingest(ctx, o); err != nil {
		t.Fatal(err)
	}
	catalog, err := s.Cases(ctx)
	if err != nil || len(catalog.Routing) != 1 {
		t.Fatalf("quote catalogue: %+v %v", catalog, err)
	}
	detail, err := s.InspectCase(ctx, catalog.Routing[0].ID)
	if err != nil || !detail.ReferencesAvailable || detail.LastQuote == nil || !reflect.DeepEqual(*detail.LastQuote, o) || detail.LastInput != nil {
		t.Fatalf("quote reference: %+v %v", detail, err)
	}
	d.ID, d.CaseKey = "case-source-timer", "case-source-timer"
	due := at.Add(2 * time.Minute)
	d.Condition = domain.Condition{Kind: "expected_event_missing", EventName: "absent", DueAt: due}
	if _, err := s.RegisterTrigger(ctx, d); err != nil {
		t.Fatal(err)
	}
	claims, err := s.ClaimTimers(ctx, "case-reader-test", due, 30*time.Second)
	if err != nil || len(claims) != 1 {
		t.Fatalf("timer claim: %+v %v", claims, err)
	}
	receipt, err := s.CompleteTimer(ctx, claims[0], due)
	if err != nil || len(receipt.Routing.Matches) != 1 {
		t.Fatalf("timer receipt: %+v %v", receipt, err)
	}
	detail, err = s.InspectCase(ctx, receipt.Routing.Matches[0].CaseID)
	if err != nil || !detail.ReferencesAvailable || detail.LastTimer == nil || !reflect.DeepEqual(detail.LastTimer.Receipt, &receipt) || detail.LastInput != nil {
		t.Fatalf("timer reference: %+v %v", detail, err)
	}
}

func caseContracts(t *testing.T, s interface {
	Store
	RoutingStore
	CaseReader
}) {
	t.Helper()
	ctx := context.Background()
	at := time.Date(2026, 1, 5, 15, 30, 0, 0, time.UTC)
	d := domain.TriggerDefinition{ID: "inspection-trigger", Owner: "operator", CaseKey: "inspection-case", Behavior: "research", Condition: domain.Condition{Kind: "event", EventName: "inspect"}, ValidFrom: at.Add(-time.Minute), ValidUntil: at.Add(time.Hour), RepeatPolicy: "coalesce", Requirements: domain.SourceRequirements{Source: "fixture", Symbol: "DEMO", EvidenceClass: "synthetic_unqualified", MaxAgeSeconds: 60}}
	if _, err := s.RegisterTrigger(ctx, d); err != nil {
		t.Fatal(err)
	}
	i := domain.RoutingInput{ID: "inspection-input", Kind: "event", Source: "fixture", Symbol: "DEMO", EvidenceClass: "synthetic_unqualified", OccurredAt: at, ReceivedAt: at, EventName: "inspect"}
	r, err := s.RouteInput(ctx, i)
	if err != nil || len(r.Matches) != 1 {
		t.Fatalf("route: %+v %v", r, err)
	}
	before, _ := s.Snapshot(ctx)
	catalog, err := s.Cases(ctx)
	if err != nil || !reflect.DeepEqual(catalog.Fixture, before.State.Case) || len(catalog.Routing) != len(before.State.Routing.Cases) {
		t.Fatalf("catalogue: %+v %v", catalog, err)
	}
	for n, c := range catalog.Routing {
		if n > 0 && catalog.Routing[n-1].ID >= c.ID {
			t.Fatal("catalogue not sorted")
		}
	}
	fixture, err := s.InspectCase(ctx, catalog.Fixture.ID)
	if err != nil || fixture.Kind != "fixture_execution" || !reflect.DeepEqual(*fixture.Fixture, before.State.Case) || !reflect.DeepEqual(*fixture.Strategy, before.State.Strategy) || fixture.Routing != nil {
		t.Fatalf("fixture: %+v %v", fixture, err)
	}
	detail, err := s.InspectCase(ctx, r.Matches[0].CaseID)
	if err != nil || detail.Kind != "routing" || !detail.ReferencesAvailable || detail.Fixture != nil || !reflect.DeepEqual(*detail.LastDefinition, d) || !reflect.DeepEqual(detail.LastInput.Input, i) || !reflect.DeepEqual(detail.LastInput.Receipt, r) {
		t.Fatalf("detail: %+v %v", detail, err)
	}
	if _, err := s.InspectCase(ctx, "missing-case"); !errors.Is(err, ErrCaseNotFound) {
		t.Fatal(err)
	}
	after, _ := s.Snapshot(ctx)
	if !reflect.DeepEqual(before, after) {
		t.Fatal("inspection mutated state or history")
	}
	later := i
	later.ID = "inspection-input-later"
	later.OccurredAt = at.Add(time.Second)
	later.ReceivedAt = later.OccurredAt
	if _, err := s.RouteInput(ctx, later); err != nil {
		t.Fatal(err)
	}
	updated, err := s.InspectCase(ctx, detail.Routing.ID)
	if err != nil || updated.Routing.MatchCount != 2 || updated.LastInput.Input.ID != later.ID || updated.LastInput.Receipt.Matches[0].CaseID != detail.Routing.ID {
		t.Fatal("coalesced case did not expose latest bound input")
	}
	detail = updated
	if err := s.CloseRoutingCase(ctx, detail.Routing.ID, "operator", at.Add(2*time.Second)); err != nil {
		t.Fatal(err)
	}
	closed, err := s.InspectCase(ctx, detail.Routing.ID)
	if err != nil || closed.Routing.Status != "closed" || !reflect.DeepEqual(closed.LastInput, detail.LastInput) {
		t.Fatal("closed case lost references")
	}
}
