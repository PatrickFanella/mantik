package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/account"
	"git.subcult.tv/subculture-collective/mantik/internal/loopbacktest"
	"git.subcult.tv/subculture-collective/mantik/internal/privatejournal"
	"git.subcult.tv/subculture-collective/mantik/internal/service"
	"git.subcult.tv/subculture-collective/mantik/internal/storage"
)

type briefCLIClock struct{ at time.Time }

func (c briefCLIClock) Now() time.Time                                  { return c.at }
func (c briefCLIClock) Wait(ctx context.Context, _ time.Duration) error { return ctx.Err() }
func briefPrivateDir(t *testing.T) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "outbox")
	if e := os.Mkdir(p, 0700); e != nil {
		t.Fatal(e)
	}
	return p
}

func TestDailyBriefCLIOutboxRetryAndRestart(t *testing.T) {
	ctx := context.Background()
	at := time.Date(2026, 11, 16, 18, 0, 0, 0, time.UTC)
	store := storage.NewMemory()
	if _, e := store.CreateCappedBook(ctx, account.DefaultCappedConfig("book", "paper", account.Cash, at.Add(-time.Hour))); e != nil {
		t.Fatal(e)
	}
	var reads, attempts atomic.Int32
	var fail atomic.Bool
	fail.Store(true)
	handler := service.HandlerWithClock(store, "api-private", briefCLIClock{at})
	api := httptest.NewServer(loopbacktest.GoClientOnly(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { reads.Add(1); handler.ServeHTTP(w, r) })))
	defer api.Close()
	var bodies [][]byte
	var keys []string
	sink := httptest.NewServer(loopbacktest.GoClientOnly(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer destination-private" {
			t.Error("missing configured notification auth")
		}
		raw, e := io.ReadAll(r.Body)
		if e != nil {
			t.Error(e)
		}
		bodies = append(bodies, raw)
		keys = append(keys, r.Header.Get("Idempotency-Key"))
		attempts.Add(1)
		if fail.Load() {
			http.Error(w, "provider-private-error", 503)
			return
		}
		w.WriteHeader(204)
	})))
	defer sink.Close()
	t.Setenv("BRIEF_TEST_TOKEN", "api-private")
	t.Setenv("BRIEF_SINK_TOKEN", "destination-private")
	config := filepath.Join(t.TempDir(), "notification.json")
	raw, _ := json.Marshal(briefNotificationConfig{sink.URL, "BRIEF_SINK_TOKEN"})
	if e := os.WriteFile(config, raw, 0600); e != nil {
		t.Fatal(e)
	}
	outbox := briefPrivateDir(t)
	args := []string{"daily-brief", "--url", api.URL, "--book", "book", "--date", "2026-11-16", "--outbox", outbox, "--token-env", "BRIEF_TEST_TOKEN", "--notification-config", config, "--notify"}
	var first, second, third, stderr bytes.Buffer
	e := Run(ctx, args, &first, &stderr)
	if e == nil || strings.Contains(e.Error(), "provider-private") || attempts.Load() != 1 {
		t.Fatal(e, attempts.Load())
	}
	if !strings.Contains(first.String(), "Equity USD: 2000.000000000") || !strings.Contains(first.String(), "SPY total-return TWR: unavailable") {
		t.Fatal(first.String())
	}
	// The outbox owns the exact snapshot. API changes and process restart do not
	// change the body, key, clocks or numbers on a failed-delivery retry.
	book, e := store.CappedBook(ctx, "book")
	if e != nil {
		t.Fatal(e)
	}
	if _, e = store.ApplyCappedBook(ctx, "book", book.Version, account.BookCommand{ID: "later-halt", Kind: "halt", At: at}); e != nil {
		t.Fatal(e)
	}
	fail.Store(false)
	t.Setenv("BRIEF_TEST_TOKEN", "")
	if e = Run(ctx, args, &second, &stderr); e != nil {
		t.Fatal(e)
	}
	if e = Run(ctx, args, &third, &stderr); e != nil {
		t.Fatal(e)
	}
	if reads.Load() != 1 || attempts.Load() != 2 || !bytes.Equal(bodies[0], bodies[1]) || keys[0] == "" || keys[0] != keys[1] || first.String() != second.String() || first.String() != third.String() {
		t.Fatal("retry changed frozen receipt", reads.Load(), attempts.Load(), keys)
	}
	files, e := os.ReadDir(outbox)
	if e != nil {
		t.Fatal(e)
	}
	for _, f := range files {
		raw, e := os.ReadFile(filepath.Join(outbox, f.Name()))
		if e != nil {
			t.Fatal(e)
		}
		if bytes.Contains(raw, []byte("api-private")) || bytes.Contains(raw, []byte("destination-private")) {
			t.Fatal("secret retained")
		}
	}
	// Changed clock/destination cannot reuse the daily receipt identity.
	changed := append(append([]string{}, args...), "--as-of", at.Add(-time.Minute).Format(time.RFC3339))
	if e = Run(ctx, changed, io.Discard, &stderr); !errors.Is(e, privatejournal.ErrConflict) {
		t.Fatal("changed retry", e)
	}
}
func TestDailyBriefDeliveryRedirectAndAmbiguousAck(t *testing.T) {
	var forwarded atomic.Int32
	target := httptest.NewServer(loopbacktest.GoClientOnly(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { forwarded.Add(1); w.WriteHeader(204) })))
	defer target.Close()
	redirect := httptest.NewServer(loopbacktest.GoClientOnly(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusTemporaryRedirect)
	})))
	defer redirect.Close()
	if _, e := briefHTTP(context.Background(), redirect.URL, "credential", []byte("{}"), "retry-key", 4096); e == nil || forwarded.Load() != 0 {
		t.Fatal(e, forwarded.Load())
	}
	for _, url := range []string{"http://example.com", "https://user:password@example.com", "https://example.com?token=private", "http://localhost:8080", "https://example.com/#private"} {
		if _, e := briefHTTPURL(url); e == nil {
			t.Fatal("unsafe endpoint accepted", url)
		}
	}
	// A 2xx larger than the bounded acknowledgement is ambiguous: retain the
	// intent and retry the same idempotency key, never write a false success.
	var keys []string
	sink := httptest.NewServer(loopbacktest.GoClientOnly(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		keys = append(keys, r.Header.Get("Idempotency-Key"))
		if len(keys) == 1 {
			_, _ = w.Write(bytes.Repeat([]byte("a"), 4097))
		} else {
			w.WriteHeader(204)
		}
	})))
	defer sink.Close()
	at := time.Date(2026, 11, 16, 18, 0, 0, 0, time.UTC)
	book, e := account.NewCappedState(account.DefaultCappedConfig("book", "paper", account.Cash, at))
	if e != nil {
		t.Fatal(e)
	}
	b, e := service.BuildDailyBrief(service.BriefSource{Book: book, AsOf: at}, nil)
	if e != nil {
		t.Fatal(e)
	}
	hash, _ := service.BriefHash(b)
	r := briefReceipt{Request: briefRequest{BookID: "book", Date: "2026-11-16", NotificationURL: sink.URL}, Brief: b, SHA256: hash}
	j, e := privatejournal.Open(briefPrivateDir(t), true, "mantik-daily-brief-outbox-v1", "brief-")
	if e != nil {
		t.Fatal(e)
	}
	defer j.Close()
	if e = deliverBrief(context.Background(), j, "book-2026-11-16", r, briefNotificationConfig{URL: sink.URL}); e == nil {
		t.Fatal("oversize acknowledgement accepted")
	}
	if e = deliverBrief(context.Background(), j, "book-2026-11-16", r, briefNotificationConfig{URL: sink.URL}); e != nil || len(keys) != 2 || keys[0] != keys[1] {
		t.Fatal(e, keys)
	}
}
func TestDailyBriefBoundedSourceAndReceiptIntegrity(t *testing.T) {
	api := httptest.NewServer(loopbacktest.GoClientOnly(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write(bytes.Repeat([]byte("x"), 1025)) })))
	defer api.Close()
	if _, e := briefHTTP(context.Background(), api.URL, "token", []byte("{}"), "", 1024); e == nil {
		t.Fatal("oversized source accepted")
	}
	dir := briefPrivateDir(t)
	j, e := privatejournal.Open(dir, true, "mantik-daily-brief-outbox-v1", "brief-")
	if e != nil {
		t.Fatal(e)
	}
	// Another invocation cannot race a publisher or an in-flight delivery.
	if _, e = privatejournal.Open(dir, false, "mantik-daily-brief-outbox-v1", "brief-"); !errors.Is(e, privatejournal.ErrBusy) {
		t.Fatal(e)
	}
	if e = j.WriteOnce("tampered.json", []byte(`{"SHA256":"invented"}`), 1024); e != nil {
		t.Fatal(e)
	}
	if _, e = loadBriefReceipt(j, "tampered.json", briefRequest{}); e == nil {
		t.Fatal("invalid retained receipt accepted")
	}
	if e = j.Close(); e != nil {
		t.Fatal(e)
	}
}
