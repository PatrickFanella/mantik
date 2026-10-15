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
	if r.URL.Host == "paper-api.alpaca.markets" && r.URL.Path == "/v2/calendar" {
		t.reads.Add(1)
		body := fmt.Sprintf(`[{"date":%q,"open":"00:00","close":"23:59"}]`, r.URL.Query().Get("start"))
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body)), Request: r}, nil
	}
	if r.URL.Host != "data.alpaca.markets" {
		return t.fallback.RoundTrip(r)
	}
	t.reads.Add(1)
	stamp := time.Now().UTC().Add(-time.Second).Format(time.RFC3339Nano)
	body := fmt.Sprintf(`{"quotes":{"SPY":{"t":%q,"ap":100,"bp":99.99,"as":10,"bs":10}}}`, stamp)
	return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body)), Request: r}, nil
}

func TestFiniteCollectorCLIThroughAuthenticatedService(t *testing.T) {
	testFiniteCollectorCLI(t, false)
}
func TestRegularSessionCollectorCLIThroughAuthenticatedService(t *testing.T) {
	testFiniteCollectorCLI(t, true)
}
func testFiniteCollectorCLI(t *testing.T, regular bool) {
	if regular {
		zone, _ := time.LoadLocation("America/New_York")
		if at := time.Now().In(zone); at.Hour() == 23 && at.Minute() == 59 {
			t.Skip("manufactured same-day regular session ends at 23:59")
		}
	}
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
	var controls, reads, publications atomic.Int32
	handler := service.Handler(store, "collector-cli-api-token")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "POST" && r.URL.Path == "/v1/feed-collectors" {
			controls.Add(1)
		}
		if r.URL.Path == "/v1/feed-evidence" && publications.Add(1) == 1 {
			saved := httptest.NewRecorder()
			handler.ServeHTTP(saved, r)
			if saved.Code != http.StatusOK {
				t.Error(saved.Body.String())
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
	if regular {
		args = append(args, "--regular-session-seconds", "60")
	}
	out, err := run(args...)
	if err != nil {
		t.Fatal(out.String(), err)
	}
	var result alpaca.CollectorStatus
	if err := json.Unmarshal(out.Bytes(), &result); err != nil || result.State != "completed" || !result.PublicationRequired || result.Published != 1 || result.Delivery.Acknowledged != 1+boolInt(regular) || !result.StopConfirmed {
		t.Fatal(out.String(), err)
	}
	if result.CalendarPublished != regular || (regular && (result.Calendar == nil || result.StopCode != "declared_count_completed")) {
		t.Fatal(result)
	}
	if strings.Contains(out.String(), "manufactured-provider-key") || strings.Contains(out.String(), "manufactured-provider-secret") {
		t.Fatal("terminal leaked credentials")
	}
	if reads.Load() != int32(1+boolInt(regular)) || controls.Load() != int32(4+2*boolInt(regular)) {
		t.Fatal("unexpected finite acquisition/control count", reads.Load(), controls.Load())
	}
	if _, err := run(args...); err != nil || reads.Load() != int32(1+boolInt(regular)) || controls.Load() != int32(4+2*boolInt(regular)) {
		t.Fatal("completed CLI repeated work", err)
	}
	if _, err := run("alpaca-collector-status", "--journal", journal, "--collector-id", spec.ID); err != nil {
		t.Fatal(err)
	}
	evidence, err := store.FeedEvidence(context.Background())
	if err != nil || len(evidence.Records) != 1+boolInt(regular) || publications.Load() != int32(2+boolInt(regular)) {
		t.Fatal("missing or repeated raw publication", err, publications.Load())
	}
	for _, record := range evidence.Records {
		if record.Fence.Generation != 1 || record.Fence.OwnerID != "finite-cli-owner" {
			t.Fatal(record.Fence)
		}
	}
	state, _ := store.Snapshot(context.Background())
	if state.ObservationCount != 0 || len(state.State.Actions.Items) != 0 {
		t.Fatal("real-feed collector reached fixture execution")
	}
}

func boolInt(value bool) int {
	if value {
		return 1
	}
	return 0
}
