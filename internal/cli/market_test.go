package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/domain"
	"git.subcult.tv/subculture-collective/mantik/internal/loopbacktest"
	"git.subcult.tv/subculture-collective/mantik/internal/routing"
	"git.subcult.tv/subculture-collective/mantik/internal/service"
	"git.subcult.tv/subculture-collective/mantik/internal/storage"
)

func TestMarketRoutingCLIProvenance(t *testing.T) {
	t.Setenv("MANTIK_API_TOKEN", "market-test-token")
	store := storage.NewMemory()
	server := httptest.NewServer(loopbacktest.GoClientOnly(service.Handler(store, "market-test-token")))
	defer server.Close()
	at := time.Date(2026, 1, 5, 15, 0, 0, 0, time.UTC)
	d := domain.TriggerDefinition{ID: "market-cli", Owner: "operator", CaseKey: "market-cli", Behavior: "research", Condition: domain.Condition{Kind: "event", EventName: "market_open"}, ValidFrom: at.Add(-time.Minute), ValidUntil: at.Add(time.Hour), RepeatPolicy: "coalesce", Requirements: domain.SourceRequirements{Source: "example-feed", Symbol: "SPY", EvidenceClass: "synthetic_unqualified", MaxAgeSeconds: 60}}
	if _, err := store.RegisterTrigger(context.Background(), d); err != nil {
		t.Fatal(err)
	}
	i := domain.RoutingInput{Kind: "event", Source: "example-feed", Symbol: "SPY", EvidenceClass: "synthetic_unqualified", OccurredAt: at, ReceivedAt: at, EventName: "market_open", Provenance: &domain.InputProvenance{RecordKey: "market-open-20260105", Revision: 1, RawRef: "artifact:market-open", SHA256: strings.Repeat("a", 64), AvailableAt: at, SourceSymbol: "SPY", MappingVersion: "equities-v1", AccessRef: "test-fixture-v1", Quality: "usable"}}
	path := filepath.Join(t.TempDir(), "input.json")
	write := func() {
		t.Helper()
		raw, err := json.Marshal(i)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, raw, 0600); err != nil {
			t.Fatal(err)
		}
	}
	write()
	run := func() (*bytes.Buffer, error) {
		out := new(bytes.Buffer)
		err := Run(context.Background(), []string{"route-input", "--derive-market-id", "--file", path, "--api-url", server.URL}, out, new(bytes.Buffer))
		return out, err
	}
	out, err := run()
	if err != nil {
		t.Fatal(err)
	}
	var receipt domain.RoutingReceipt
	if err := json.Unmarshal(out.Bytes(), &receipt); err != nil || receipt.InputID != routing.MarketInputID(i) || len(receipt.Matches) != 1 {
		t.Fatal("CLI lost derived identity or routing")
	}
	out, err = run()
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(out.Bytes(), &receipt); err != nil || !receipt.Duplicate {
		t.Fatal("CLI retry repeated work")
	}
	i.ID = "changed-identity"
	write()
	if _, err := run(); err == nil {
		t.Fatal("CLI replaced explicit conflicting identity")
	}
	i.ID = ""
	i.Provenance = nil
	write()
	if _, err := run(); err == nil {
		t.Fatal("CLI derived identity without provenance")
	}
}
