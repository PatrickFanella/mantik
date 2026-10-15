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
