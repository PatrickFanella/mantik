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

	"git.subcult.tv/subculture-collective/mantik/internal/loopbacktest"
	"git.subcult.tv/subculture-collective/mantik/internal/polygonref"
)

type polygonTestTransport struct {
	base     *url.URL
	delegate http.RoundTripper
}

func (t polygonTestTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	if r.URL.Scheme != "https" || r.URL.Host != "api.polygon.io" || r.Method != http.MethodGet {
		return nil, fmt.Errorf("unexpected polygon target %s", r.URL.Host)
	}
	clone := r.Clone(r.Context())
	target := *r.URL
	target.Scheme, target.Host = t.base.Scheme, t.base.Host
	clone.URL = &target
	return t.delegate.RoundTrip(clone)
}

func TestPolygonReferenceCLICollectsWithPrivateCredential(t *testing.T) {
	const key = "manufactured-cli-polygon-key"
	var calls atomic.Int32
	server := httptest.NewServer(loopbacktest.GoClientOnly(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.URL.Path != "/v3/reference/splits" || r.URL.Query().Get("apiKey") != key || r.URL.Query().Get("ticker") != "AAPL" {
			t.Error("unexpected polygon request", r.URL.Path)
		}
		_, _ = w.Write([]byte(`{"results":[{"execution_date":"2020-08-31","id":"P1","split_from":1,"split_to":4,"ticker":"AAPL"}],"status":"OK","request_id":"manufactured"}`))
	})))
	defer server.Close()
	base, _ := url.Parse(server.URL)
	previous := http.DefaultTransport
	http.DefaultTransport = polygonTestTransport{base, previous}
	defer func() { http.DefaultTransport = previous }()
	root := t.TempDir()
	journal := filepath.Join(root, "polygon-journal")
	specPath := filepath.Join(root, "spec.json")
	credentials := filepath.Join(root, "polygon.json")
	spec := `{"id":"aapl-splits","method":"` + polygonref.MethodSplits + `","symbol":"AAPL","start":"2020-01-01","end":"2020-12-31","access_ref":"manufactured-polygon-account"}`
	if err := os.WriteFile(specPath, []byte(spec), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(credentials, []byte(`{"api_key":"`+key+`"}`), 0644); err != nil {
		t.Fatal(err)
	}
	var out, errOut bytes.Buffer
	args := []string{"polygon-corporate-collect", "--journal", journal, "--spec", specPath, "--credentials-file", credentials}
	if err := Run(context.Background(), args, &out, &errOut); err == nil || calls.Load() != 0 {
		t.Fatal("non-private credential file accepted", err)
	}
	if err := os.Chmod(credentials, 0600); err != nil {
		t.Fatal(err)
	}
	if err := Run(context.Background(), args, &out, &errOut); err != nil {
		t.Fatal(out.String(), err)
	}
	var status polygonref.Status
	if err := json.Unmarshal(out.Bytes(), &status); err != nil || status.State != "retained" || status.Snapshot == nil || len(status.Snapshot.Splits) != 1 || status.Snapshot.Splits[0].SplitTo != "4" || status.CoverageComplete {
		t.Fatal("CLI collection failed", out.String(), err)
	}
	out.Reset()
	if err := Run(context.Background(), args, &out, &errOut); err != nil || calls.Load() != 1 {
		t.Fatal("completed identity refetched", err)
	}
	out.Reset()
	if err := Run(context.Background(), []string{"polygon-corporate-collection", "--journal", journal, "--collection-id", "aapl-splits"}, &out, &errOut); err != nil || !strings.Contains(out.String(), `"parse_status": "parsed"`) {
		t.Fatal("inspection failed", out.String(), err)
	}
	entries, _ := os.ReadDir(journal)
	for _, e := range entries {
		raw, _ := os.ReadFile(filepath.Join(journal, e.Name()))
		if strings.Contains(string(raw), key) {
			t.Fatal("credential retained in journal", e.Name())
		}
	}
	if strings.Contains(out.String()+errOut.String(), key) {
		t.Fatal("credential printed")
	}
	for _, bad := range [][]string{
		{"polygon-corporate-collect", "--journal", journal, "--spec", specPath},
		{"polygon-corporate-collection", "--journal", journal},
	} {
		if err := Run(context.Background(), bad, &out, &errOut); err == nil {
			t.Fatal("invalid invocation accepted", bad)
		}
	}
}
