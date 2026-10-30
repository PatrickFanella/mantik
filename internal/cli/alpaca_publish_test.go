package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
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

func TestCapturePublicationCLIRecoversLostReplyAfterStop(t *testing.T) {
	t.Setenv("MANTIK_API_TOKEN", "capture-publish-token")
	ctx := context.Background()
	store := storage.NewMemory()
	handler := service.Handler(store, "capture-publish-token")
	var publications atomic.Int32
	server := httptest.NewServer(loopbacktest.GoClientOnly(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/feed-evidence" && publications.Add(1) == 1 {
			recorded := httptest.NewRecorder()
			handler.ServeHTTP(recorded, r)
			if recorded.Code != 200 {
				t.Error(recorded.Body.String())
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
	received := time.Now().UTC()
	raw := fmt.Sprintf(`{"quotes":{"SPY":{"t":%q,"ap":100,"bp":99.99,"as":10,"bs":10}}}`, received.Add(-time.Second).Format(time.RFC3339Nano))
	r := alpaca.Recording{Schema: alpaca.RecordingSchema, Currency: "USD", CaptureID: "publish-cli", Capability: "latest_quotes", Feed: "iex", Symbols: []string{"SPY"}, Revision: 1, ReceivedAt: received, EvidenceClass: "prospective_market_unqualified", AccessRef: "manufactured-access", RawRef: "artifact:manufactured-test", Original: raw, SHA256: alpaca.Digest(raw), MaxQuoteAgeSeconds: 60}
	if _, err := j.Retain(r); err != nil {
		t.Fatal(err)
	}
	journalID := j.ID
	_ = j.Close()
	initial, _ := store.FeedCollectors(ctx)
	registration := feedcollector.Registration{ID: "spy-iex", Provider: "alpaca", Feed: "iex", Symbol: "SPY", EvidenceClass: r.EvidenceClass, JournalID: journalID, AccessRef: r.AccessRef, LeaseSeconds: 300}
	for _, c := range []feedcollector.Command{{RequestID: "register", Operation: "register", RegistrationID: registration.ID, Registration: &registration}, {RequestID: "claim", Operation: "claim", RegistrationID: registration.ID, OwnerID: "owner"}} {
		if _, err := store.FeedCollectorCommand(ctx, initial.ReceiverID, c, time.Now); err != nil {
			t.Fatal(err)
		}
	}
	run := func(args ...string) (*bytes.Buffer, error) {
		out := new(bytes.Buffer)
		err := Run(ctx, args, out, new(bytes.Buffer))
		return out, err
	}
	common := []string{"--journal", root, "--api-url", server.URL, "--receiver-id", initial.ReceiverID, "--collector-registration-id", registration.ID, "--collector-owner-id", "owner", "--collector-generation", "1"}
	if out, err := run(append([]string{"alpaca-submit"}, common...)...); err != nil {
		t.Fatal(out.String(), err)
	}
	args := append(append([]string{"alpaca-publish"}, common...), "--capture-id", r.CaptureID)
	if _, err := run(args...); err == nil {
		t.Fatal("lost publication reply reported success")
	}
	if _, err := store.FeedCollectorCommand(ctx, initial.ReceiverID, feedcollector.Command{RequestID: "stop", Operation: "stop", RegistrationID: registration.ID, OwnerID: "owner", Generation: 1}, time.Now); err != nil {
		t.Fatal(err)
	}
	out, err := run(args...)
	if err != nil {
		t.Fatal(err)
	}
	var receipt storage.FeedEvidenceReceipt
	if err := json.Unmarshal(out.Bytes(), &receipt); err != nil || !receipt.Duplicate {
		t.Fatal(out.String(), err)
	}
	snapshot, _ := store.FeedEvidence(ctx)
	if len(snapshot.Records) != 1 || publications.Load() != 2 {
		t.Fatal("capture publication multiplied")
	}
	if out, err := run("feed-coverage", "--api-url", server.URL, "--collector-registration-id", registration.ID); err != nil {
		t.Fatal(out.String(), err)
	}
}
