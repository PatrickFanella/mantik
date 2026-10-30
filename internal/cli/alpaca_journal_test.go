package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/alpaca"
	"git.subcult.tv/subculture-collective/mantik/internal/domain"
	"git.subcult.tv/subculture-collective/mantik/internal/loopbacktest"
	"git.subcult.tv/subculture-collective/mantik/internal/service"
	"git.subcult.tv/subculture-collective/mantik/internal/storage"
)

func TestJournalCLIReconcilesLostHTTPResponse(t *testing.T) {
	t.Setenv("MANTIK_API_TOKEN", "journal-cli-token")
	store := storage.NewMemory()
	handler := service.Handler(store, "journal-cli-token")
	var posts atomic.Int32
	server := httptest.NewServer(loopbacktest.GoClientOnly(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/research-inputs" && posts.Add(1) == 1 {
			recorded := httptest.NewRecorder()
			handler.ServeHTTP(recorded, r)
			if recorded.Code != 200 {
				t.Errorf("expected committed input, got %d", recorded.Code)
			}
			conn, _, err := w.(http.Hijacker).Hijack()
			if err != nil {
				t.Error(err)
				return
			}
			_ = conn.Close()
			return
		}
		handler.ServeHTTP(w, r)
	})))
	defer server.Close()
	raw := `{"quotes":{"SPY":{"t":"2026-10-14T15:00:00Z","ap":100,"bp":99.99,"as":10,"bs":10}}}`
	record := alpaca.Recording{Schema: alpaca.RecordingSchema, Currency: "USD", CaptureID: "cli-journal", Capability: "latest_quotes", Feed: "iex", Symbols: []string{"SPY"}, Revision: 1, ReceivedAt: time.Date(2026, 10, 14, 15, 0, 1, 0, time.UTC), EvidenceClass: "synthetic_unqualified", AccessRef: "manufactured-fixture", RawRef: "artifact:journal-cli", Original: raw, SHA256: alpaca.Digest(raw), MaxQuoteAgeSeconds: 60}
	root := t.TempDir()
	path := filepath.Join(root, "recording.json")
	encoded, _ := json.Marshal(record)
	if err := os.WriteFile(path, encoded, 0600); err != nil {
		t.Fatal(err)
	}
	journal := filepath.Join(root, "journal")
	run := func(args ...string) (*bytes.Buffer, error) {
		t.Helper()
		out := new(bytes.Buffer)
		err := Run(context.Background(), args, out, new(bytes.Buffer))
		return out, err
	}
	if _, err := run("alpaca-retain", "--recording", path, "--sha256", record.SHA256, "--journal", journal); err != nil {
		t.Fatal(err)
	}
	receiver, _ := store.ResearchSnapshot(context.Background())
	submit := []string{"alpaca-submit", "--journal", journal, "--api-url", server.URL, "--receiver-id", receiver.ID, "--limit", "1"}
	if _, err := run(submit...); err == nil {
		t.Fatal("lost reply reported complete")
	}
	out, err := run("alpaca-journal", "--journal", journal)
	if err != nil {
		t.Fatal(err)
	}
	var status alpaca.JournalStatus
	if err := json.Unmarshal(out.Bytes(), &status); err != nil || status.Pending != 1 {
		t.Fatal("lost reply removed pending capture")
	}
	out, err = run(submit...)
	if err != nil {
		t.Fatal(err)
	}
	var delivered alpaca.DeliveryResult
	if err := json.Unmarshal(out.Bytes(), &delivered); err != nil || delivered.Pending != 0 || delivered.Reconciled != 1 || posts.Load() != 1 {
		t.Fatal("lost reply was not reconciled without resubmission", delivered, posts.Load())
	}
	after, _ := store.Snapshot(context.Background())
	if after.ObservationCount != 0 || len(after.State.Actions.Items) != 0 {
		t.Fatal("journal delivery changed fixture economics")
	}
	var direct domain.ResearchSnapshot
	if err := requestWithQuery(context.Background(), server.URL, "/v1/research-receiver", nil, &direct, nil, 1<<20); err != nil || direct.ID != receiver.ID {
		t.Fatal("receiver handshake failed")
	}
}
