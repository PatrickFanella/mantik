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
	"sync/atomic"
	"testing"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/alpaca"
)

type corporateActionTransport struct {
	base     *url.URL
	delegate http.RoundTripper
}

func (t corporateActionTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	if r.URL.Host != "data.alpaca.markets" || r.URL.Path != "/v1/corporate-actions" || r.Method != "GET" {
		return nil, fmt.Errorf("unexpected corporate-action test target")
	}
	c := r.Clone(r.Context())
	target := *r.URL
	target.Scheme, target.Host = t.base.Scheme, t.base.Host
	c.URL = &target
	return t.delegate.RoundTrip(c)
}
func TestCorporateActionExistingCollectionCLIAndInspection(t *testing.T) {
	var calls atomic.Int32
	raw := `{"corporate_actions":{"cash_dividends":[{"id":"incomplete","symbol":"SPY","rate":0.125}]},"next_page_token":null}`
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.URL.Query().Get("data_quality") != "all" {
			t.Error("missing query contract")
		}
		_, _ = fmt.Fprint(w, raw)
	}))
	defer server.Close()
	base, _ := url.Parse(server.URL)
	previous := http.DefaultTransport
	http.DefaultTransport = corporateActionTransport{base, previous}
	defer func() { http.DefaultTransport = previous }()
	root := t.TempDir()
	journal := filepath.Join(root, "journal")
	specPath := filepath.Join(root, "spec.json")
	credentials := filepath.Join(root, "credentials.json")
	if err := os.WriteFile(credentials, []byte(`{"key":"manufactured-key","secret":"manufactured-secret"}`), 0600); err != nil {
		t.Fatal(err)
	}
	spec := `{"id":"cli-corporate-actions","capability":"corporate_actions","feed":"iex","symbols":["SPY"],"start":"2026-10-14","end":"2026-10-14","access_ref":"manufactured-access-contract","max_quote_age_seconds":60}`
	if err := os.WriteFile(specPath, []byte(spec), 0600); err != nil {
		t.Fatal(err)
	}
	args := []string{"alpaca-collect", "--journal", journal, "--spec", specPath, "--credentials-file", credentials}
	var out, errOut bytes.Buffer
	for n := 0; n < 2; n++ {
		out.Reset()
		if err := Run(context.Background(), args, &out, &errOut); err != nil {
			t.Fatal(err)
		}
	}
	if calls.Load() != 1 {
		t.Fatal("existing CLI refetched completed identity")
	}
	var status alpaca.CollectionStatus
	if err := json.Unmarshal(out.Bytes(), &status); err != nil || status.State != "retained" {
		t.Fatal(out.String(), err)
	}
	j, err := alpaca.OpenExistingJournal(journal)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = j.Close() }()
	snapshots, err := j.CorporateActionSnapshots(time.Now().UTC())
	if err != nil || len(snapshots) != 1 || snapshots[0].EconomicApplicationReady || snapshots[0].CoverageComplete {
		t.Fatal(snapshots, err)
	}
	// The existing normalize command exposes the typed source snapshot with no
	// new command or caller-supplied financial authority.
	r := alpaca.Recording{Schema: alpaca.RecordingSchema, Currency: "USD", CaptureID: "manual-source", Capability: "corporate_actions", Feed: alpaca.CorporateActionFeed, Symbols: []string{"SPY"}, Start: "2026-10-14", End: "2026-10-14", Revision: 1, ReceivedAt: time.Now().UTC(), EvidenceClass: "synthetic_unqualified", AccessRef: "manufactured", RawRef: "artifact:manufactured", Original: raw, SHA256: alpaca.Digest(raw), MaxQuoteAgeSeconds: 60}
	data, _ := json.Marshal(r)
	recordPath := filepath.Join(root, "record.json")
	if err := os.WriteFile(recordPath, data, 0600); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	if err := Run(context.Background(), []string{"alpaca-normalize", "--recording", recordPath, "--sha256", r.SHA256}, &out, &errOut); err != nil {
		t.Fatal(err)
	}
	var normalized alpaca.Result
	if err := json.Unmarshal(out.Bytes(), &normalized); err != nil || normalized.CorporateActions == nil || len(normalized.Inputs) != 0 || normalized.CorporateActions.CoverageComplete {
		t.Fatal(out.String(), err)
	}
}

func TestCorporateActionExistingCLIContinuationRetainsCursorAndIntent(t *testing.T) {
	var calls atomic.Int32
	cursor := "cursor+/=&? space"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		token := r.URL.Query().Get("page_token")
		switch token {
		case "":
			raw, _ := json.Marshal(map[string]any{"corporate_actions": map[string]any{"cash_dividends": []any{map[string]any{"id": "a"}}}, "next_page_token": cursor})
			_, _ = w.Write(raw)
		case cursor:
			_, _ = fmt.Fprint(w, `{"corporate_actions":{"cash_dividends":[{"id":"b"}]},"next_page_token":null}`)
		default:
			t.Error("cursor changed", token)
		}
	}))
	defer server.Close()
	base, _ := url.Parse(server.URL)
	previous := http.DefaultTransport
	http.DefaultTransport = corporateActionTransport{base, previous}
	defer func() { http.DefaultTransport = previous }()
	root := t.TempDir()
	journal := filepath.Join(root, "journal")
	specPath := filepath.Join(root, "spec.json")
	credentials := filepath.Join(root, "credentials.json")
	if err := os.WriteFile(credentials, []byte(`{"key":"manufactured-key","secret":"manufactured-secret"}`), 0600); err != nil {
		t.Fatal(err)
	}
	run := func(id, token string) {
		t.Helper()
		spec := alpaca.CollectionSpec{ID: id, Capability: "corporate_actions", Feed: "iex", Symbols: []string{"SPY"}, Start: "2026-10-14", End: "2026-10-14", AccessRef: "manufactured-access", MaxQuoteAgeSeconds: 60, PageToken: token}
		raw, _ := json.Marshal(spec)
		if err := os.WriteFile(specPath, raw, 0600); err != nil {
			t.Fatal(err)
		}
		var out, errOut bytes.Buffer
		if err := Run(context.Background(), []string{"alpaca-collect", "--journal", journal, "--spec", specPath, "--credentials-file", credentials}, &out, &errOut); err != nil {
			t.Fatal(err)
		}
		var status alpaca.CollectionStatus
		if err := json.Unmarshal(out.Bytes(), &status); err != nil || status.State != "retained" {
			t.Fatal(out.String(), err)
		}
	}
	run("cli-root", "")
	if calls.Load() != 1 {
		t.Fatal("command automatically followed cursor")
	}
	run("cli-page", cursor)
	run("cli-page", cursor)
	if calls.Load() != 2 {
		t.Fatal("continuation was duplicated")
	}
	j, err := alpaca.OpenExistingJournal(journal)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = j.Close() }()
	chain, err := j.CorporateActionPageChain("cli-root", 1, time.Now().UTC())
	if err != nil || !chain.PaginationTerminated || len(chain.Pages) != 2 || chain.Pages[1].PageToken != cursor || chain.CoverageComplete || chain.EconomicApplicationReady {
		t.Fatal(chain, err)
	}
}
