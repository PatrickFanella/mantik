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
)

func exceptionFixturePage(id string, more bool) domain.ExceptionPage {
	return domain.ExceptionPage{Mode: "paper_fixture", Scope: "retained_exceptions", NextAfter: id, HasMore: more, Items: []domain.OperationalException{{ID: id, Status: "open", Code: "fixture_failure", EvidenceRefs: []string{"worker:fixture"}, RecoveryAttempts: []domain.RecoveryAttempt{{RequestID: "fixture-recovery", Operation: "recheck", Outcome: "not_recovered"}}}}}
}

func TestExceptionSupervisionJourney(t *testing.T) {
	var queries []storage.ExceptionQuery
	fetch := func(_ context.Context, q storage.ExceptionQuery) (domain.ExceptionPage, error) {
		queries = append(queries, q)
		if len(queries) == 2 {
			return domain.ExceptionPage{}, errors.New("offline")
		}
		if q.After == "" {
			return exceptionFixturePage("exception-a", true), nil
		}
		return exceptionFixturePage("exception-b", false), nil
	}
	var out bytes.Buffer
	commands := "d exception-b\nd exception-a\nn\nd exception-a\nn\nr\nd exception-b\nn\nb\nq\n"
	if err := superviseExceptions(context.Background(), io.NopCloser(strings.NewReader(commands)), &out, 1, fetch); err != nil {
		t.Fatal(err)
	}
	want := []storage.ExceptionQuery{{Limit: 1}, {After: "exception-a", Limit: 1}, {After: "exception-a", Limit: 1}, {Limit: 1}}
	if !reflect.DeepEqual(queries, want) {
		t.Fatalf("requests advanced incorrectly: %+v", queries)
	}
	for _, want := range []string{"no request sent", "STALE PAGE", "STALE: navigation and details", "fixture-recovery", "worker:fixture", "End of retained traversal", "not current readiness", "no recovery request sent"} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("missing %q: %s", want, out.String())
		}
	}
}

func TestExceptionSupervisionInvalidPages(t *testing.T) {
	valid := exceptionFixturePage("exception-b", false)
	bad := []domain.ExceptionPage{valid, valid, valid, valid, valid, valid}
	bad[0].Scope = "readiness"
	bad[1].Mode = "live"
	bad[2].NextAfter = "wrong"
	bad[3].Items = []domain.OperationalException{{ID: "exception-a"}}
	bad[3].NextAfter = "exception-a"
	bad[4].Items = append(append([]domain.OperationalException{}, valid.Items...), domain.OperationalException{ID: "exception-c"})
	bad[4].NextAfter = "exception-c"
	bad[5].Items = nil
	bad[5].HasMore = true
	bad[5].NextAfter = "exception-a"
	for _, p := range bad {
		if err := validateExceptionPage(p, storage.ExceptionQuery{After: "exception-a", Limit: 1}); err == nil {
			t.Fatalf("accepted invalid page: %+v", p)
		}
	}
	fetch := func(context.Context, storage.ExceptionQuery) (domain.ExceptionPage, error) { return bad[0], nil }
	var out bytes.Buffer
	if err := superviseExceptions(context.Background(), io.NopCloser(strings.NewReader("n\nd exception-b\n")), &out, 1, fetch); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out.String(), "READBACK OK") || strings.Contains(out.String(), "RETAINED DETAIL FROM") {
		t.Fatal("invalid page enabled readback or details")
	}
}

func TestExceptionSupervisionEmptyAndCancellation(t *testing.T) {
	page := domain.ExceptionPage{Mode: "paper_fixture", Scope: "retained_exceptions", Items: []domain.OperationalException{}}
	var out bytes.Buffer
	if err := superviseExceptions(context.Background(), io.NopCloser(strings.NewReader("n\nq\n")), &out, 10, func(context.Context, storage.ExceptionQuery) (domain.ExceptionPage, error) { return page, nil }); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "not a current readiness assessment") {
		t.Fatal("empty page implied readiness")
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	in, w := io.Pipe()
	defer w.Close()
	ready := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- superviseExceptions(ctx, in, io.Discard, 10, func(context.Context, storage.ExceptionQuery) (domain.ExceptionPage, error) {
			close(ready)
			return page, nil
		})
	}()
	<-ready
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("cancelled viewer blocked input")
	}
}
