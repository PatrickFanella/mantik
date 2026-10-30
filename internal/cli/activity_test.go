package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"git.subcult.tv/subculture-collective/mantik/internal/domain"
	"git.subcult.tv/subculture-collective/mantik/internal/loopbacktest"
	"git.subcult.tv/subculture-collective/mantik/internal/service"
	"git.subcult.tv/subculture-collective/mantik/internal/storage"
	"git.subcult.tv/subculture-collective/mantik/internal/workflow"
)

func TestActivityCLIParity(t *testing.T) {
	ctx := context.Background()
	m := storage.NewMemory()
	for _, o := range workflow.Fixtures() {
		if _, err := m.Ingest(ctx, o); err != nil {
			t.Fatal(err)
		}
	}
	s := httptest.NewServer(loopbacktest.GoClientOnly(service.Handler(m, "test-activity")))
	defer s.Close()
	t.Setenv("MANTIK_API_TOKEN", "test-activity")
	var out, errOut bytes.Buffer
	if err := Run(ctx, []string{"activity", "--api-url", s.URL, "--limit", "2", "--format", "json"}, &out, &errOut); err != nil {
		t.Fatal(err)
	}
	var actual domain.ActivityPage
	if err := json.Unmarshal(out.Bytes(), &actual); err != nil {
		t.Fatal(err)
	}
	expected, _ := m.Activity(ctx, storage.ActivityQuery{Limit: 2})
	if !reflect.DeepEqual(actual, expected) {
		t.Fatal("CLI changed structured events or cursors")
	}
	out.Reset()
	if err := Run(ctx, []string{"activity", "--api-url", s.URL, "--limit", "1"}, &out, &errOut); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "HISTORY THROUGH") || !strings.Contains(out.String(), "--format json") {
		t.Fatal(out.String())
	}
	// Invalid arguments must fail locally, before contacting any service.
	for _, args := range [][]string{{"--limit", "0"}, {"--limit", "101"}, {"--after", "-1"}, {"--through", "bad"}, {"--after", "1", "--through", "0"}, {"--format", "xml"}, {"--api-url", "http://user:password@localhost"}, {"unexpected"}} {
		if err := Run(ctx, append([]string{"activity"}, args...), &out, &errOut); err == nil {
			t.Fatalf("accepted %v", args)
		}
	}
}

func TestActivityCLIEscapesTerminalControls(t *testing.T) {
	s := httptest.NewServer(loopbacktest.GoClientOnly(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" || r.URL.Path != "/v1/activity" {
			t.Errorf("unexpected mutation or path: %s %s", r.Method, r.URL.Path)
		}
		_ = json.NewEncoder(w).Encode(domain.ActivityPage{Mode: "paper_fixture", Items: []domain.ActivityEvent{{Sequence: 1, Event: domain.Event{ID: "bad\x1b[2J", Kind: "kind\nforged"}}}})
	})))
	defer s.Close()
	var out bytes.Buffer
	if err := Run(context.Background(), []string{"activity", "--api-url", s.URL}, &out, &out); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out.String(), "\x1b") || strings.Contains(out.String(), "kind\nforged") {
		t.Fatal("untrusted event text can control terminal")
	}
}
