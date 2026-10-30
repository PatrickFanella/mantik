package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/domain"
	"git.subcult.tv/subculture-collective/mantik/internal/loopbacktest"
	"git.subcult.tv/subculture-collective/mantik/internal/service"
	"git.subcult.tv/subculture-collective/mantik/internal/storage"
)

func TestExceptionsCLI(t *testing.T) {
	ctx := context.Background()
	m := storage.NewMemory()
	at := time.Date(2026, 1, 5, 15, 0, 0, 0, time.UTC)
	if err := m.ReportWorker(ctx, domain.WorkerReport{WorkerHealth: domain.WorkerHealth{Capability: "action_delivery", WorkerID: "fixture-worker", At: at, IntervalMillis: 1000, ErrorCode: "fixture_failure"}}); err != nil {
		t.Fatal(err)
	}
	s := httptest.NewServer(loopbacktest.GoClientOnly(service.Handler(m, "exception-cli")))
	defer s.Close()
	t.Setenv("MANTIK_API_TOKEN", "exception-cli")
	var out bytes.Buffer
	if err := Run(ctx, []string{"exceptions", "--api-url", s.URL, "--format", "json", "--limit", "1"}, &out, &out); err != nil {
		t.Fatal(err)
	}
	var actual domain.ExceptionPage
	if err := json.Unmarshal(out.Bytes(), &actual); err != nil {
		t.Fatal(err)
	}
	expected, _ := m.Exceptions(ctx, storage.ExceptionQuery{Limit: 1})
	if !reflect.DeepEqual(actual, expected) {
		t.Fatal("CLI changed retained records or cursors")
	}
	out.Reset()
	if err := Run(ctx, []string{"exceptions", "--api-url", s.URL, "--after", actual.NextAfter}, &out, &out); err != nil || !strings.Contains(out.String(), "not a current readiness assessment") {
		t.Fatalf("table: %s %v", out.String(), err)
	}
	for _, args := range [][]string{{"--limit", "0"}, {"--limit", "101"}, {"--after", "bad cursor"}, {"--format", "xml"}, {"unexpected"}} {
		if err := Run(ctx, append([]string{"exceptions"}, args...), &out, &out); err == nil {
			t.Fatalf("accepted %v", args)
		}
	}
}
