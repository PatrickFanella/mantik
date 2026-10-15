package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/alpaca"
	"git.subcult.tv/subculture-collective/mantik/internal/feedcollector"
	"git.subcult.tv/subculture-collective/mantik/internal/service"
	"git.subcult.tv/subculture-collective/mantik/internal/storage"
)

type collectorProviderTransport struct {
	fallback http.RoundTripper
	reads    *atomic.Int32
}

func (t collectorProviderTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	if r.URL.Host != "data.alpaca.markets" {
		return t.fallback.RoundTrip(r)
	}
	t.reads.Add(1)
	stamp := time.Now().UTC().Add(-time.Second).Format(time.RFC3339Nano)
	body := fmt.Sprintf(`{"quotes":{"SPY":{"t":%q,"ap":100,"bp":99.99,"as":10,"bs":10}}}`, stamp)
	return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body)), Request: r}, nil
}

func TestFiniteCollectorCLIThroughAuthenticatedService(t *testing.T) {
	t.Setenv("MANTIK_API_TOKEN", "collector-cli-api-token")
	root := t.TempDir()
	journal := filepath.Join(root, "journal")
	run := func(args ...string) (*bytes.Buffer, error) {
		out := new(bytes.Buffer)
		err := Run(context.Background(), args, out, new(bytes.Buffer))
		return out, err
	}
	if _, err := run("alpaca-journal", "--journal", journal); err == nil {
		t.Fatal("missing journal recreated")
	}
	if _, err := run("alpaca-journal", "--journal", journal, "--init"); err != nil {
		t.Fatal(err)
	}
	j, err := alpaca.OpenExistingJournal(journal)
	if err != nil {
		t.Fatal(err)
	}
	journalID := j.ID
	_ = j.Close()
	store := storage.NewMemory()
	snapshot, _ := store.FeedCollectors(context.Background())
	r := feedcollector.Registration{ID: "spy-iex", Provider: "alpaca", Feed: "iex", Symbol: "SPY", EvidenceClass: "prospective_market_unqualified", JournalID: journalID, AccessRef: "manufactured-access-contract", LeaseSeconds: 300}
	if _, err := store.FeedCollectorCommand(context.Background(), snapshot.ReceiverID, feedcollector.Command{RequestID: "register", Operation: "register", RegistrationID: r.ID, Registration: &r}, time.Now); err != nil {
		t.Fatal(err)
	}
	var controls, reads atomic.Int32
	handler := service.Handler(store, "collector-cli-api-token")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "POST" && r.URL.Path == "/v1/feed-collectors" {
			controls.Add(1)
		}
		handler.ServeHTTP(w, r)
	}))
	defer server.Close()
	previous := http.DefaultTransport
	http.DefaultTransport = collectorProviderTransport{fallback: previous, reads: &reads}
	t.Cleanup(func() { http.DefaultTransport = previous })
	credentials := filepath.Join(root, "credentials.json")
	if err := os.WriteFile(credentials, []byte(`{"key":"manufactured-provider-key","secret":"manufactured-provider-secret"}`), 0600); err != nil {
		t.Fatal(err)
	}
	spec := alpaca.SessionSpec{ID: "finite-cli", Count: 1, IntervalSeconds: 1, Collection: alpaca.CollectionSpec{Capability: "latest_quotes", Feed: "iex", Symbols: []string{"SPY"}, AccessRef: r.AccessRef, MaxQuoteAgeSeconds: 60}}
	path := filepath.Join(root, "session.json")
	raw, _ := json.Marshal(spec)
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	args := []string{"alpaca-run-collector", "--journal", journal, "--spec", path, "--credentials-file", credentials, "--api-url", server.URL, "--receiver-id", snapshot.ReceiverID, "--collector-registration-id", r.ID, "--collector-owner-id", "finite-cli-owner"}
	out, err := run(args...)
	if err != nil {
		t.Fatal(out.String(), err)
	}
	var result alpaca.CollectorStatus
	if err := json.Unmarshal(out.Bytes(), &result); err != nil || result.State != "completed" || result.Delivery.Acknowledged != 1 || !result.StopConfirmed {
		t.Fatal(out.String(), err)
	}
	if strings.Contains(out.String(), "manufactured-provider-key") || strings.Contains(out.String(), "manufactured-provider-secret") {
		t.Fatal("terminal leaked credentials")
	}
	if reads.Load() != 1 || controls.Load() != 4 {
		t.Fatal("unexpected finite acquisition/control count", reads.Load(), controls.Load())
	}
	if _, err := run(args...); err != nil || reads.Load() != 1 || controls.Load() != 4 {
		t.Fatal("completed CLI repeated work", err)
	}
	if _, err := run("alpaca-collector-status", "--journal", journal, "--collector-id", spec.ID); err != nil {
		t.Fatal(err)
	}
	state, _ := store.Snapshot(context.Background())
	if state.ObservationCount != 0 || len(state.State.Actions.Items) != 0 {
		t.Fatal("real-feed collector reached fixture execution")
	}
}
