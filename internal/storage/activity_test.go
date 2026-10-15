package storage

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/domain"
	"git.subcult.tv/subculture-collective/mantik/internal/workflow"
)

func TestMemoryActivity(t *testing.T) {
	m := NewMemory()
	ctx := context.Background()
	empty, err := m.Activity(ctx, ActivityQuery{Limit: 1})
	if err != nil || empty.Through != 0 || empty.NextAfter != 0 || empty.HasMore || len(empty.Items) != 0 {
		t.Fatalf("empty: %+v %v", empty, err)
	}
	for _, o := range workflow.Fixtures() {
		if _, err := m.Ingest(ctx, o); err != nil {
			t.Fatal(err)
		}
	}
	activityContracts(t, m)
	// Returned event details must not alias canonical history.
	page, _ := m.Activity(ctx, ActivityQuery{Limit: 1})
	page.Items[0].Event.ID = "changed"
	again, _ := m.Activity(ctx, ActivityQuery{Limit: 1})
	if again.Items[0].Event.ID == "changed" {
		t.Fatal("history exposed mutable storage")
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := m.Activity(cancelled, ActivityQuery{Limit: 1}); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}

func activityContracts(t *testing.T, store interface {
	Store
	ActivityReader
}) {
	t.Helper()
	ctx := context.Background()
	before, err := store.Snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	first, err := store.Activity(ctx, ActivityQuery{Limit: 1})
	if err != nil || len(first.Items) != 1 || !first.HasMore {
		t.Fatalf("first: %+v %v", first, err)
	}
	// Append after taking the boundary, as a disconnected reader's service would.
	o := workflow.Fixtures()[3]
	o.ID = "activity-later"
	o.OccurredAt = before.State.LastOccurredAt.Add(time.Minute)
	o.ReceivedAt = o.OccurredAt
	if _, err := store.Ingest(ctx, o); err != nil {
		t.Fatal(err)
	}
	items := append([]domain.ActivityEvent{}, first.Items...)
	page := first
	for page.HasMore {
		previous := page.NextAfter
		page, err = store.Activity(ctx, ActivityQuery{After: previous, Through: &first.Through, Limit: 2})
		if err != nil || page.Through != first.Through || len(page.Items) > 2 || page.NextAfter <= previous {
			t.Fatalf("next: %+v %v", page, err)
		}
		items = append(items, page.Items...)
	}
	if len(items) != len(before.Events) {
		t.Fatalf("history lost or included later event: %d != %d", len(items), len(before.Events))
	}
	for i, item := range items {
		if !reflect.DeepEqual(item.Event, before.Events[i]) || i > 0 && item.Sequence <= items[i-1].Sequence {
			t.Fatal("event order or structured details changed")
		}
	}
	retry, err := store.Activity(ctx, ActivityQuery{Limit: 1, Through: &first.Through})
	if err != nil || !reflect.DeepEqual(retry, first) {
		t.Fatal("reconnect changed page")
	}
	end, err := store.Activity(ctx, ActivityQuery{After: page.NextAfter, Through: &first.Through, Limit: 1})
	if err != nil || len(end.Items) != 0 || end.HasMore {
		t.Fatalf("end: %+v %v", end, err)
	}
	newer, err := store.Activity(ctx, ActivityQuery{After: page.NextAfter, Limit: 100})
	if err != nil || len(newer.Items) == 0 || newer.Through <= first.Through {
		t.Fatalf("new history: %+v %v", newer, err)
	}
	zero := int64(0)
	fixedEmpty, err := store.Activity(ctx, ActivityQuery{Through: &zero, Limit: 1})
	if err != nil || fixedEmpty.Through != 0 || len(fixedEmpty.Items) != 0 {
		t.Fatal("zero boundary was treated as current")
	}
	future := newer.Through + 1
	for _, q := range []ActivityQuery{{Limit: 0}, {Limit: 101}, {After: -1, Limit: 1}, {After: 1, Through: &zero, Limit: 1}, {Through: &future, Limit: 1}, {After: future, Limit: 1}} {
		if _, err := store.Activity(ctx, q); !errors.Is(err, ErrActivityQuery) {
			t.Fatalf("accepted invalid query: %+v %v", q, err)
		}
	}
}
