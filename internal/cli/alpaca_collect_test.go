package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/loopbacktest"
)

func TestCollectionCLIRequiresExplicitPrivateCredentialsAndSpec(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "credentials.json")
	raw := []byte(`{"key":"manufactured-key","secret":"manufactured-secret"}`)
	if err := os.WriteFile(path, raw, 0644); err != nil {
		t.Fatal(err)
	}
	if _, _, err := collectionCredentials(path); err == nil {
		t.Fatal("public credentials admitted")
	}
	if err := os.Chmod(path, 0600); err != nil {
		t.Fatal(err)
	}
	key, secret, err := collectionCredentials(path)
	if err != nil || key != "manufactured-key" || secret != "manufactured-secret" {
		t.Fatal("private credentials not accepted", err)
	}
	link := filepath.Join(root, "link.json")
	if err := os.Symlink(path, link); err != nil {
		t.Fatal(err)
	}
	if _, _, err := collectionCredentials(link); err == nil {
		t.Fatal("credential leaf symlink accepted")
	}
	if err := os.WriteFile(path, []byte(`{"key":"manufactured-secret","secret":42}`), 0600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := collectionCredentials(path); err == nil || strings.Contains(err.Error(), "manufactured-secret") {
		t.Fatal("parse error leaked secret", err)
	}
	for _, args := range [][]string{{"alpaca-collect", "--journal", filepath.Join(root, "journal")}, {"alpaca-collect", "--journal", filepath.Join(root, "journal"), "--spec", path, "--credentials-file", path}, {"alpaca-collection", "--journal", filepath.Join(root, "missing"), "--collection-id", "abc"}} {
		var out, errOut bytes.Buffer
		if err := Run(context.Background(), args, &out, &errOut); err == nil {
			t.Fatal("invalid collection admitted", args)
		}
	}
	if _, err := os.Stat(filepath.Join(root, "journal")); !os.IsNotExist(err) {
		t.Fatal("invalid preflight created journal")
	}
	if _, err := os.Stat(filepath.Join(root, "missing")); !os.IsNotExist(err) {
		t.Fatal("inspection recreated journal")
	}
	var out, errOut bytes.Buffer
	if err := Run(context.Background(), []string{"alpaca-collect", "--help"}, &out, &errOut); err != nil || !strings.Contains(errOut.String(), "credentials-file") {
		t.Fatal("help contract unavailable", err)
	}
}
func TestCollectionSpecStrictJSONAndSize(t *testing.T) {
	path := filepath.Join(t.TempDir(), "spec.json")
	for _, raw := range []string{`{"key":"unexpected"}`, `{} {}`, strings.Repeat("x", 16385)} {
		if err := os.WriteFile(path, []byte(raw), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := readCollectionSpec(path); err == nil {
			t.Fatal("invalid spec admitted")
		}
	}
}

// Only this test redirects the standard transport to a manufactured local
// server. The production command accepts no endpoint override.
type collectionTestTransport struct {
	base     *url.URL
	delegate http.RoundTripper
}

func (t collectionTestTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	if r.URL.Host != "data.alpaca.markets" || r.URL.Path != "/v2/stocks/quotes/latest" || r.Method != "GET" {
		return nil, fmt.Errorf("unexpected provider target")
	}
	clone := r.Clone(r.Context())
	target := *r.URL
	target.Scheme, target.Host = t.base.Scheme, t.base.Host
	clone.URL = &target
	return t.delegate.RoundTrip(clone)
}
func TestCollectionCLICompleteFlowAndFailureInspection(t *testing.T) {
	var calls atomic.Int32
	var fail atomic.Bool
	server := httptest.NewServer(loopbacktest.GoClientOnly(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Header.Get("APCA-API-KEY-ID") != "manufactured-key" || r.Header.Get("APCA-API-SECRET-KEY") != "manufactured-secret" {
			t.Error("credentials not delivered to test provider")
		}
		if fail.Load() {
			w.WriteHeader(403)
			_, _ = fmt.Fprint(w, "private entitlement failure")
			return
		}
		_, _ = fmt.Fprintf(w, `{"quotes":{"SPY":{"t":%q,"ap":100.01,"bp":100,"as":10,"bs":10}}}`, time.Now().UTC().Add(-time.Second).Format(time.RFC3339Nano))
	})))
	defer server.Close()
	base, _ := url.Parse(server.URL)
	previous := http.DefaultTransport
	http.DefaultTransport = collectionTestTransport{base, previous}
	defer func() { http.DefaultTransport = previous }()
	root := t.TempDir()
	journal := filepath.Join(root, "journal")
	specPath := filepath.Join(root, "spec.json")
	credentials := filepath.Join(root, "credentials.json")
	if err := os.WriteFile(credentials, []byte(`{"key":"manufactured-key","secret":"manufactured-secret"}`), 0600); err != nil {
		t.Fatal(err)
	}
	spec := `{"id":"cli-quotes","capability":"latest_quotes","feed":"iex","symbols":["SPY"],"access_ref":"manufactured-access-contract","max_quote_age_seconds":60}`
	if err := os.WriteFile(specPath, []byte(spec), 0600); err != nil {
		t.Fatal(err)
	}
	args := []string{"alpaca-collect", "--journal", journal, "--spec", specPath, "--credentials-file", credentials}
	var out, errOut bytes.Buffer
	if err := Run(context.Background(), args, &out, &errOut); err != nil {
		t.Fatal(err)
	}
	var result struct {
		State    string `json:"state"`
		Attempts int    `json:"attempts"`
	}
	if err := json.Unmarshal(out.Bytes(), &result); err != nil || result.State != "retained" || result.Attempts != 1 {
		t.Fatal("CLI capture failed", out.String(), err)
	}
	out.Reset()
	if err := Run(context.Background(), args, &out, &errOut); err != nil || calls.Load() != 1 {
		t.Fatal("CLI completed identity refetched", err)
	}
	out.Reset()
	if err := Run(context.Background(), []string{"alpaca-journal", "--journal", journal}, &out, &errOut); err != nil || !strings.Contains(out.String(), `"pending": 1`) {
		t.Fatal("capture not available for later submission", out.String(), err)
	}
	fail.Store(true)
	if err := os.WriteFile(specPath, []byte(strings.Replace(spec, "cli-quotes", "cli-failed", 1)), 0600); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	if err := Run(context.Background(), args, &out, &errOut); err == nil || calls.Load() != 2 || !strings.Contains(out.String(), `"state": "failed"`) || strings.Contains(out.String(), "private entitlement") || strings.Contains(err.Error(), "manufactured-secret") {
		t.Fatal("failure not bounded or leaked body", out.String(), err)
	}
	out.Reset()
	if err := Run(context.Background(), []string{"alpaca-collection", "--journal", journal, "--collection-id", "cli-failed"}, &out, &errOut); err != nil || !strings.Contains(out.String(), `"last_http_status": 403`) {
		t.Fatal("failed collection not inspectable", out.String(), err)
	}
}

func TestSessionCLICompleteFiniteFlowAndStatus(t *testing.T) {
	var calls atomic.Int32
	var last atomic.Int64
	server := httptest.NewServer(loopbacktest.GoClientOnly(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		now := time.Now().UnixNano()
		previous := last.Swap(now)
		if calls.Add(1) > 1 && time.Duration(now-previous) < time.Second {
			t.Error("finite CLI session violated declared pacing")
		}
		_, _ = fmt.Fprintf(w, `{"quotes":{"SPY":{"t":%q,"ap":100.01,"bp":100,"as":10,"bs":10}}}`, time.Now().UTC().Add(-time.Second).Format(time.RFC3339Nano))
	})))
	defer server.Close()
	if status, err := loopbacktest.Probe(context.Background(), server.URL); err != nil || status != http.StatusNotFound || calls.Load() != 0 {
		t.Fatal("unrelated loopback client counted as a paced request", status, err)
	}
	base, _ := url.Parse(server.URL)
	previous := http.DefaultTransport
	http.DefaultTransport = collectionTestTransport{base, previous}
	defer func() { http.DefaultTransport = previous }()
	root := t.TempDir()
	journal := filepath.Join(root, "journal")
	specPath := filepath.Join(root, "session.json")
	credentials := filepath.Join(root, "credentials.json")
	if err := os.WriteFile(credentials, []byte(`{"key":"manufactured-key","secret":"manufactured-secret"}`), 0600); err != nil {
		t.Fatal(err)
	}
	spec := `{"id":"cli-session","count":2,"interval_seconds":1,"collection":{"capability":"latest_quotes","feed":"iex","symbols":["SPY"],"access_ref":"manufactured-access-contract","max_quote_age_seconds":60}}`
	if err := os.WriteFile(specPath, []byte(spec), 0600); err != nil {
		t.Fatal(err)
	}
	args := []string{"alpaca-session", "--journal", journal, "--spec", specPath, "--credentials-file", credentials}
	var out, errOut bytes.Buffer
	if err := Run(context.Background(), args, &out, &errOut); err != nil || calls.Load() != 2 || !strings.Contains(out.String(), `"state": "completed"`) || !strings.Contains(out.String(), `"retained": 2`) {
		t.Fatal("finite session did not complete", out.String(), err)
	}
	out.Reset()
	if err := Run(context.Background(), []string{"alpaca-session-status", "--journal", journal, "--session-id", "cli-session"}, &out, &errOut); err != nil || !strings.Contains(out.String(), `"retained": 2`) {
		t.Fatal("session inspection failed", out.String(), err)
	}
	out.Reset()
	if err := Run(context.Background(), args, &out, &errOut); err != nil || calls.Load() != 2 {
		t.Fatal("session CLI refetched completed identity", err)
	}
	out.Reset()
	if err := Run(context.Background(), []string{"alpaca-journal", "--journal", journal}, &out, &errOut); err != nil || !strings.Contains(out.String(), `"pending": 2`) {
		t.Fatal("session did not retain separate pending inputs", out.String(), err)
	}
}
func TestSessionCLISpecAndHelpBoundaries(t *testing.T) {
	path := filepath.Join(t.TempDir(), "spec.json")
	for _, raw := range []string{`{"unexpected":true}`, `{} {}`, strings.Repeat("x", 16385)} {
		if err := os.WriteFile(path, []byte(raw), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := readSessionSpec(path); err == nil {
			t.Fatal("session parser admitted invalid JSON")
		}
	}
	for _, command := range []string{"alpaca-session", "alpaca-session-status"} {
		var out, errOut bytes.Buffer
		if err := Run(context.Background(), []string{command, "--help"}, &out, &errOut); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(errOut.String(), "journal") {
			t.Fatal("session help missing ownership contract")
		}
	}
}
