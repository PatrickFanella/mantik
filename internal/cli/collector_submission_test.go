package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/alpaca"
	"git.subcult.tv/subculture-collective/mantik/internal/feedcollector"
	"git.subcult.tv/subculture-collective/mantik/internal/loopbacktest"
	"git.subcult.tv/subculture-collective/mantik/internal/service"
	"git.subcult.tv/subculture-collective/mantik/internal/storage"
)

type collectorTestClock struct{ unix atomic.Int64 }

func (c *collectorTestClock) Now() time.Time                            { return time.Unix(c.unix.Load(), 0).UTC() }
func (c *collectorTestClock) Wait(context.Context, time.Duration) error { return nil }

func TestCollectorJournalLostReplyAndBinding(t *testing.T) {
	t.Setenv("MANTIK_API_TOKEN", "collector-test-token")
	ctx := context.Background()
	s := storage.NewMemory()
	clock := &collectorTestClock{}
	clock.unix.Store(time.Date(2026, 10, 15, 13, 30, 0, 0, time.UTC).Unix())
	handler := service.HandlerWithClock(s, "collector-test-token", clock)
	var posts atomic.Int32
	var manual atomic.Int32
	server := httptest.NewServer(loopbacktest.GoClientOnly(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/research-inputs" {
			manual.Add(1)
		}
		if r.URL.Path == "/v1/collector-inputs" && posts.Add(1) == 1 {
			recorder := httptest.NewRecorder()
			handler.ServeHTTP(recorder, r)
			if recorder.Code != 200 {
				t.Error(recorder.Body.String())
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
	root := filepath.Join(t.TempDir(), "journal")
	j, err := alpaca.OpenJournal(root)
	if err != nil {
		t.Fatal(err)
	}
	raw := `{"quotes":{"SPY":{"t":"2026-10-15T13:29:59Z","ap":100,"bp":99.99,"as":10,"bs":10}}}`
	record := alpaca.Recording{Schema: alpaca.RecordingSchema, Currency: "USD", CaptureID: "collector-cli", Capability: "latest_quotes", Feed: "iex", Symbols: []string{"SPY"}, Revision: 1, ReceivedAt: clock.Now(), EvidenceClass: "prospective_market_unqualified", AccessRef: "access-one", RawRef: "artifact:manufactured-collector-test", Original: raw, SHA256: alpaca.Digest(raw), MaxQuoteAgeSeconds: 60}
	if _, err := j.Retain(record); err != nil {
		t.Fatal(err)
	}
	r := feedcollector.Registration{ID: "spy-iex", Provider: "alpaca", Feed: "iex", Symbol: "SPY", EvidenceClass: record.EvidenceClass, JournalID: j.ID, AccessRef: record.AccessRef, LeaseSeconds: 5}
	_ = j.Close()
	initial, _ := s.FeedCollectors(ctx)
	for _, c := range []feedcollector.Command{{RequestID: "register", Operation: "register", RegistrationID: r.ID, Registration: &r}, {RequestID: "claim", Operation: "claim", RegistrationID: r.ID, OwnerID: "worker-one"}} {
		if _, err := s.FeedCollectorCommand(ctx, initial.ReceiverID, c, clock.Now); err != nil {
			t.Fatal(err)
		}
	}
	args := []string{"alpaca-submit", "--journal", root, "--api-url", server.URL, "--receiver-id", initial.ReceiverID, "--collector-registration-id", r.ID, "--collector-owner-id", "worker-one", "--collector-generation", "1"}
	run := func(args []string) (*bytes.Buffer, error) {
		out := new(bytes.Buffer)
		err := Run(ctx, args, out, new(bytes.Buffer))
		return out, err
	}
	if _, err := run(args); err == nil {
		t.Fatal("lost response reported success")
	}
	clock.unix.Add(6)
	out, err := run(args)
	if err != nil {
		t.Fatal(err)
	}
	var delivered alpaca.DeliveryResult
	if err := json.Unmarshal(out.Bytes(), &delivered); err != nil || delivered.Reconciled != 1 || delivered.Pending != 0 || posts.Load() != 1 || manual.Load() != 0 {
		t.Fatal("expired lost reply not reconciled", out.String(), err)
	}
	// Omitting the fence or switching registrations cannot weaken a retained
	// fenced journal binding, even when all inputs are already acknowledged.
	plain := args[:7]
	if _, err := run(plain); err == nil {
		t.Fatal("fenced binding silently downgraded")
	}
	changed := append([]string{}, args...)
	changed[8] = "other-registration"
	if _, err := run(changed); err == nil {
		t.Fatal("registration rebound")
	}
	if manual.Load() != 0 {
		t.Fatal("collector command fell back to manual endpoint")
	}
}
