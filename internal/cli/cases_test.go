package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"git.subcult.tv/subculture-collective/mantik/internal/domain"
	"git.subcult.tv/subculture-collective/mantik/internal/service"
	"git.subcult.tv/subculture-collective/mantik/internal/storage"
)

func TestCasesCLI(t *testing.T) {
	ctx := context.Background()
	m := storage.NewMemory()
	server := httptest.NewServer(service.Handler(m, "case-cli-token"))
	defer server.Close()
	t.Setenv("MANTIK_API_TOKEN", "case-cli-token")
	var out bytes.Buffer
	if err := Run(ctx, []string{"cases", "--api-url", server.URL, "--format", "json"}, &out, &out); err != nil {
		t.Fatal(err)
	}
	var catalog domain.CaseCatalog
	if err := json.Unmarshal(out.Bytes(), &catalog); err != nil {
		t.Fatal(err)
	}
	expected, _ := m.Cases(ctx)
	if !reflect.DeepEqual(catalog, expected) {
		t.Fatal("CLI changed catalogue")
	}
	out.Reset()
	if err := Run(ctx, []string{"cases", "--api-url", server.URL, "--id", catalog.Fixture.ID}, &out, &out); err != nil {
		t.Fatal(err)
	}
	var detail domain.CaseInspection
	if err := json.Unmarshal(out.Bytes(), &detail); err != nil {
		t.Fatal(err)
	}
	want, _ := m.InspectCase(ctx, catalog.Fixture.ID)
	if !reflect.DeepEqual(detail, want) {
		t.Fatal("CLI changed inspection")
	}
	out.Reset()
	if err := Run(ctx, []string{"cases", "--api-url", server.URL}, &out, &out); err != nil || !strings.Contains(out.String(), "fixture_execution") {
		t.Fatalf("table: %s %v", out.String(), err)
	}
	for _, args := range [][]string{{"--id", "../bad"}, {"--format", "xml"}, {"--api-url", "http://user:password@localhost"}, {"extra"}} {
		if err := Run(ctx, append([]string{"cases"}, args...), &out, &out); err == nil {
			t.Fatalf("accepted %v", args)
		}
	}
}
