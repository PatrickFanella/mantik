package cli

import (
	"bytes"
	"context"
	"errors"
	"io"
	"reflect"
	"strings"
	"testing"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/domain"
	"git.subcult.tv/subculture-collective/mantik/internal/storage"
	"git.subcult.tv/subculture-collective/mantik/internal/workflow"
)

func TestSupervisionActivityJourney(t *testing.T) {
	m := storage.NewMemory()
	ctx := context.Background()
	for _, o := range workflow.Fixtures() {
		if _, err := m.Ingest(ctx, o); err != nil {
			t.Fatal(err)
		}
	}
	var requests []storage.ActivityQuery
	fetch := func(ctx context.Context, q storage.ActivityQuery) (domain.ActivityPage, error) {
		requests = append(requests, q)
		if len(requests) == 2 {
			return domain.ActivityPage{}, errors.New("offline")
		}
		return m.Activity(ctx, q)
	}
	var out bytes.Buffer
	err := superviseActivity(ctx, io.NopCloser(strings.NewReader("d 1\nn\nd 1\nn\nr\nb\nu\nq\n")), &out, 1, fetch)
	if err != nil {
		t.Fatal(err)
	}
	if len(requests) != 4 || requests[0].Through != nil || requests[1].After != 1 || !reflect.DeepEqual(requests[1], requests[2]) || requests[3].After != 0 || requests[3].Through != nil {
		t.Fatalf("cursor or retry changed: %+v", requests)
	}
	for _, want := range []string{"\"sequence\": 1", "STALE: activity request failed", "STALE: navigation and details", "Finish this fixed history", "READBACK OK"} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("missing %q: %s", want, out.String())
		}
	}
	// The viewer itself did not ingest, mutate or acquire authority.
	after, _ := m.Snapshot(ctx)
	if after.ObservationCount != int64(len(workflow.Fixtures())) {
		t.Fatal("viewer mutated service")
	}
}

func TestSupervisionAppendAfterFixedHistory(t *testing.T) {
	ctx := context.Background()
	m := storage.NewMemory()
	var queries []storage.ActivityQuery
	fetch := func(ctx context.Context, q storage.ActivityQuery) (domain.ActivityPage, error) {
		queries = append(queries, q)
		if len(queries) == 2 {
			if _, err := m.Ingest(ctx, workflow.Fixtures()[0]); err != nil {
				t.Fatal(err)
			}
		}
		return m.Activity(ctx, q)
	}
	var out bytes.Buffer
	if err := superviseActivity(ctx, io.NopCloser(strings.NewReader("n\nu\nd 1\nd 999\nunknown\nq\n")), &out, 10, fetch); err != nil {
		t.Fatal(err)
	}
	if len(queries) != 2 || queries[1].After != 0 || queries[1].Through != nil {
		t.Fatalf("append read used old boundary: %+v", queries)
	}
	if !strings.Contains(out.String(), "\"sequence\": 1") || !strings.Contains(out.String(), "Choose an event sequence") || !strings.Contains(out.String(), "Unknown command") {
		t.Fatal(out.String())
	}
}

func TestSupervisionInitialFailureAndEOF(t *testing.T) {
	calls := 0
	var out bytes.Buffer
	fetch := func(context.Context, storage.ActivityQuery) (domain.ActivityPage, error) {
		calls++
		return domain.ActivityPage{}, errors.New("offline")
	}
	if err := superviseActivity(context.Background(), io.NopCloser(strings.NewReader("n\nd 1\nr\n")), &out, 10, fetch); err != nil {
		t.Fatal(err)
	}
	if calls != 2 || strings.Contains(out.String(), "READBACK OK") {
		t.Fatal("failure treated as successful history")
	}
}

func TestSupervisionCancellationUnblocksInput(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	in, writer := io.Pipe()
	defer writer.Close()
	ready := make(chan struct{})
	done := make(chan error, 1)
	fetch := func(context.Context, storage.ActivityQuery) (domain.ActivityPage, error) {
		close(ready)
		return domain.ActivityPage{Mode: "paper_fixture"}, nil
	}
	go func() { done <- superviseActivity(ctx, in, io.Discard, 10, fetch) }()
	<-ready
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("cancelled viewer left blocked input")
	}
}

func TestSupervisionCommandBound(t *testing.T) {
	var out bytes.Buffer
	fetch := func(context.Context, storage.ActivityQuery) (domain.ActivityPage, error) {
		return domain.ActivityPage{}, nil
	}
	if err := superviseActivity(context.Background(), io.NopCloser(strings.NewReader(strings.Repeat("x", 300)+"\n")), &out, 10, fetch); err == nil {
		t.Fatal("unbounded command accepted")
	}
}

func TestActivityReadbackValidation(t *testing.T) {
	boundary := int64(20)
	q := storage.ActivityQuery{After: 3, Through: &boundary, Limit: 2}
	valid := domain.ActivityPage{Mode: "paper_fixture", Through: 20, NextAfter: 12, HasMore: true,
		Items: []domain.ActivityEvent{{Sequence: 7}, {Sequence: 12}}}
	if err := validateActivityPage(valid, q); err != nil {
		t.Fatalf("noncontiguous sequences rejected: %v", err)
	}
	for name, mutate := range map[string]func(*domain.ActivityPage){
		"mode":                func(p *domain.ActivityPage) { p.Mode = "live" },
		"changed boundary":    func(p *domain.ActivityPage) { p.Through++ },
		"too many":            func(p *domain.ActivityPage) { p.Items = append(p.Items, domain.ActivityEvent{Sequence: 13}) },
		"duplicate":           func(p *domain.ActivityPage) { p.Items[1].Sequence = 7 },
		"reversed":            func(p *domain.ActivityPage) { p.Items[1].Sequence = 6 },
		"before cursor":       func(p *domain.ActivityPage) { p.Items[0].Sequence = 3 },
		"past boundary":       func(p *domain.ActivityPage) { p.Items[1].Sequence = 21 },
		"wrong cursor":        func(p *domain.ActivityPage) { p.NextAfter++ },
		"empty continuation":  func(p *domain.ActivityPage) { p.Items = nil; p.NextAfter = 3 },
		"short continuation":  func(p *domain.ActivityPage) { p.Items = p.Items[:1]; p.NextAfter = 7 },
		"continuation at end": func(p *domain.ActivityPage) { p.Items[1].Sequence = 20; p.NextAfter = 20 },
	} {
		t.Run(name, func(t *testing.T) {
			p := valid
			p.Items = append([]domain.ActivityEvent(nil), valid.Items...)
			mutate(&p)
			if err := validateActivityPage(p, q); err == nil {
				t.Fatal("malformed readback accepted")
			}
		})
	}
	for _, p := range []domain.ActivityPage{
		{Mode: "paper_fixture", Through: 3, NextAfter: 3},
		{Mode: "paper_fixture", Through: 20, NextAfter: 20, Items: []domain.ActivityEvent{{Sequence: 20}}},
	} {
		if err := validateActivityPage(p, storage.ActivityQuery{After: 3, Limit: 2}); err != nil {
			t.Fatal(err)
		}
	}
}

func TestMalformedActivityReadbackRetainsPageAndRetry(t *testing.T) {
	var queries []storage.ActivityQuery
	fetch := func(_ context.Context, q storage.ActivityQuery) (domain.ActivityPage, error) {
		queries = append(queries, q)
		switch len(queries) {
		case 1:
			return domain.ActivityPage{Mode: "paper_fixture", Through: 9, NextAfter: 2, HasMore: true,
				Items: []domain.ActivityEvent{{Sequence: 2}}}, nil
		case 2:
			// A successful HTTP response must not move the fixed boundary or
			// expose a new event when its cursor contract is inconsistent.
			return domain.ActivityPage{Mode: "paper_fixture", Through: 10, NextAfter: 10,
				Items: []domain.ActivityEvent{{Sequence: 10}}}, nil
		default:
			return domain.ActivityPage{Mode: "paper_fixture", Through: 9, NextAfter: 9,
				Items: []domain.ActivityEvent{{Sequence: 9}}}, nil
		}
	}
	var out bytes.Buffer
	if err := superviseActivity(context.Background(), io.NopCloser(strings.NewReader("n\nd 10\nn\nu\nr\nd 9\nq\n")), &out, 1, fetch); err != nil {
		t.Fatal(err)
	}
	if len(queries) != 3 || !reflect.DeepEqual(queries[1], queries[2]) || queries[2].Through == nil || *queries[2].Through != 9 || queries[2].After != 2 {
		t.Fatalf("malformed readback changed pending cursor: %+v", queries)
	}
	if strings.Count(out.String(), "READBACK OK") != 2 || !strings.Contains(out.String(), "STALE: navigation and details") || strings.Contains(out.String(), "\"sequence\": 10") || !strings.Contains(out.String(), "\"sequence\": 9") {
		t.Fatal(out.String())
	}
}
