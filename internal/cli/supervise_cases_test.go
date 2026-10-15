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
)

func TestCaseSupervisionJourney(t *testing.T) {
	catalog := domain.CaseCatalog{Mode: "paper_fixture", Fixture: domain.DecisionCase{ID: "fixture", Status: "waiting"}, Routing: []domain.RoutingCase{{ID: "research-one", Status: "open", Behavior: "research"}, {ID: "research-two", Status: "closed", Behavior: "research"}}}
	loads := 0
	var details []string
	fetch := func(context.Context) (domain.CaseCatalog, error) { loads++; return catalog, nil }
	inspect := func(_ context.Context, id string) (domain.CaseInspection, error) {
		details = append(details, id)
		if len(details) == 1 {
			return domain.CaseInspection{}, errors.New("offline")
		}
		return domain.CaseInspection{Kind: "routing", Routing: &catalog.Routing[0], ReferencesAvailable: false}, nil
	}
	var out bytes.Buffer
	commands := "d research-one\nd fixture\nn\nd fixture\nr\nn\nd research-one\nn\np\nr\nq\n"
	if err := superviseCases(context.Background(), io.NopCloser(strings.NewReader(commands)), &out, 1, fetch, inspect); err != nil {
		t.Fatal(err)
	}
	if loads != 3 || !reflect.DeepEqual(details, []string{"fixture", "research-one"}) {
		t.Fatalf("unexpected readbacks: %d %v", loads, details)
	}
	for _, want := range []string{"no request sent", "STALE: case detail request failed", "STALE: navigation and details", "CURRENT DETAIL", "\"references_available\": false", "ITEMS 3..3 OF 3"} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("missing %q: %s", want, out.String())
		}
	}
}

func TestCaseSupervisionFailureCapacityAndEOF(t *testing.T) {
	for _, oversize := range []bool{false, true} {
		loads, details := 0, 0
		fetch := func(context.Context) (domain.CaseCatalog, error) {
			loads++
			if oversize {
				return domain.CaseCatalog{Routing: make([]domain.RoutingCase, 261)}, nil
			}
			return domain.CaseCatalog{}, errors.New("offline")
		}
		inspect := func(context.Context, string) (domain.CaseInspection, error) {
			details++
			return domain.CaseInspection{}, nil
		}
		var out bytes.Buffer
		if err := superviseCases(context.Background(), io.NopCloser(strings.NewReader("n\np\nd anything\nr\n")), &out, 10, fetch, inspect); err != nil {
			t.Fatal(err)
		}
		if loads != 2 || details != 0 || strings.Contains(out.String(), "READBACK OK") {
			t.Fatal("failed catalogue allowed stale work")
		}
	}
}

func TestCaseSupervisionCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	in, writer := io.Pipe()
	defer writer.Close()
	ready := make(chan struct{})
	done := make(chan error, 1)
	fetch := func(context.Context) (domain.CaseCatalog, error) { close(ready); return domain.CaseCatalog{}, nil }
	go func() { done <- superviseCases(ctx, in, io.Discard, 10, fetch, nil) }()
	<-ready
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("case viewer left blocked input")
	}
}

func TestCasePageBoundariesAndQuoting(t *testing.T) {
	c := domain.CaseCatalog{Mode: "paper_fixture", Fixture: domain.DecisionCase{ID: "fixture\x1b[2J", Status: "waiting"}, Routing: []domain.RoutingCase{{ID: "one", Status: "open"}, {ID: "two", Status: "closed"}}}
	if caseOnPage(c, 0, 1, "one") || !caseOnPage(c, 1, 1, "one") || caseOnPage(c, 1, 1, "two") {
		t.Fatal("off-page selection accepted")
	}
	var out bytes.Buffer
	if err := renderCasePage(&out, c, 0, 1, false); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out.String(), "\x1b") || strings.Contains(out.String(), "routing  ") {
		t.Fatal("control text or off-page rows rendered")
	}
	out.Reset()
	if err := renderCasePage(&out, c, 1, 1, true); err != nil || !strings.Contains(out.String(), "STALE CATALOGUE") {
		t.Fatal("stale display missing")
	}
}

func TestSupervisionViewValidation(t *testing.T) {
	var out bytes.Buffer
	if err := Run(context.Background(), []string{"supervise", "--view", "unknown"}, &out, &out); err == nil {
		t.Fatal("unknown view accepted")
	}
}
