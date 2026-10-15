package storage

import (
	"context"
	"errors"
	"reflect"
	"slices"
	"testing"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/domain"
)

func TestMemoryExceptionPages(t *testing.T) {
	m := NewMemory()
	exceptionContracts(t, m)
	page, _ := m.Exceptions(context.Background(), ExceptionQuery{Limit: 100})
	page.Items[0].EvidenceRefs[0] = "changed"
	again, _ := m.Exceptions(context.Background(), ExceptionQuery{Limit: 100})
	if again.Items[0].EvidenceRefs[0] == "changed" {
		t.Fatal("exception read aliases evidence")
	}
}

func TestPostgresContractsExceptionPages(t *testing.T) {
	p := timerPostgres(t, "exception_pages")
	exceptionContracts(t, p)
	// Prove this readback remains usable without access to the audit ledger.
	ctx := context.Background()
	if _, err := p.pool.Exec(ctx, "ALTER TABLE mantik_events RENAME TO hidden_exception_test_events"); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if _, err := p.pool.Exec(ctx, "ALTER TABLE hidden_exception_test_events RENAME TO mantik_events"); err != nil {
			t.Error(err)
		}
	}()
	if _, err := p.Exceptions(ctx, ExceptionQuery{Limit: 1}); err != nil {
		t.Fatalf("readback depended on event history: %v", err)
	}
}

func exceptionContracts(t *testing.T, s interface {
	Store
	OperationalStore
	ExceptionReader
}) {
	t.Helper()
	ctx := context.Background()
	empty, err := s.Exceptions(ctx, ExceptionQuery{Limit: 1})
	if err != nil || empty.Items == nil || len(empty.Items) != 0 || empty.HasMore || empty.NextAfter != "" {
		t.Fatalf("empty: %+v %v", empty, err)
	}
	at := time.Date(2026, 1, 5, 15, 0, 0, 0, time.UTC)
	for _, cap := range []string{"action_delivery", "protective_monitoring"} {
		r := health(cap, at)
		r.ErrorCode = "fixture_failure"
		if err := s.ReportWorker(ctx, r); err != nil {
			t.Fatal(err)
		}
	}
	snapshot, err := s.Snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	ids := []string{}
	for id := range snapshot.State.Routing.Timers.Exceptions {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	if len(ids) < 2 {
		t.Fatal("missing exception fixtures")
	}
	if _, err := s.RecoverException(ctx, ids[0], "exception-readback-recovery", "recheck", at.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	before, _ := s.Snapshot(ctx)
	var items []domain.OperationalException
	after := ""
	for {
		p, err := s.Exceptions(ctx, ExceptionQuery{After: after, Limit: 1})
		if err != nil || len(p.Items) > 1 || p.Scope != "retained_exceptions" {
			t.Fatalf("page: %+v %v", p, err)
		}
		items = append(items, p.Items...)
		if !p.HasMore {
			after = p.NextAfter
			break
		}
		if p.NextAfter <= after {
			t.Fatal("cursor did not advance")
		}
		after = p.NextAfter
	}
	if len(items) != len(ids) {
		t.Fatal("pagination lost or duplicated exceptions")
	}
	for i, id := range ids {
		if !reflect.DeepEqual(items[i], before.State.Routing.Timers.Exceptions[id]) {
			t.Fatal("evidence or recovery attempts changed")
		}
	}
	if len(items[0].RecoveryAttempts) != 1 {
		t.Fatal("recovery attempt omitted")
	}
	end, err := s.Exceptions(ctx, ExceptionQuery{After: after, Limit: 100})
	if err != nil || len(end.Items) != 0 || end.HasMore || end.NextAfter != after {
		t.Fatal("end cursor changed")
	}
	full, err := s.Exceptions(ctx, ExceptionQuery{Limit: 100})
	if err != nil || !reflect.DeepEqual(full.Items, items) {
		t.Fatal("page-size parity failed")
	}
	for _, q := range []ExceptionQuery{{Limit: 0}, {Limit: 101}, {After: "bad cursor", Limit: 1}} {
		if _, err := s.Exceptions(ctx, q); !errors.Is(err, ErrExceptionQuery) {
			t.Fatalf("invalid query accepted: %+v %v", q, err)
		}
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := s.Exceptions(cancelled, ExceptionQuery{Limit: 1}); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	current, _ := s.Snapshot(ctx)
	if !reflect.DeepEqual(before, current) {
		t.Fatal("exception readback mutated persisted state")
	}
}
