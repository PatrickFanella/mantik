package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/domain"
	"git.subcult.tv/subculture-collective/mantik/internal/paperstrategy"
	"git.subcult.tv/subculture-collective/mantik/internal/service"
	"git.subcult.tv/subculture-collective/mantik/internal/storage"
)

type strategyHTTPBackend interface {
	storage.Store
	storage.NewsStore
	storage.PaperStrategyStore
}

func strategyHTTPDefinition() paperstrategy.Definition {
	return paperstrategy.Definition{ID: "baseline", Method: paperstrategy.Method, Symbol: "SPY", Feed: "iex", InputEvidenceClass: "synthetic_unqualified", EntryAtOrBelowCents: 10000, Quantity: 10, Safety: domain.Safety{MaxNotionalCents: 100000, StopCents: 9500, TargetCents: 11000, MaxHoldingSeconds: 3600, MaxQuoteAgeSeconds: 60}}
}
func TestPaperStrategyCLIAndHTTP(t *testing.T) { paperStrategyHTTP(t, storage.NewMemory()) }
func paperStrategyHTTP(t *testing.T, s strategyHTTPBackend) {
	t.Helper()
	ctx := context.Background()
	n, err := s.NewsCases(ctx)
	if err != nil {
		t.Fatal(err)
	}
	before, _ := s.Snapshot(ctx)
	server := httptest.NewServer(service.Handler(s, "strategy-test-token"))
	defer server.Close()
	t.Setenv("MANTIK_API_TOKEN", "strategy-test-token")
	d := strategyHTTPDefinition()
	raw, _ := json.Marshal(d)
	file := filepath.Join(t.TempDir(), "definition.json")
	if err := os.WriteFile(file, raw, 0600); err != nil {
		t.Fatal(err)
	}
	run := func(args ...string) []byte {
		t.Helper()
		var out, diag bytes.Buffer
		args = append(args, "--api-url", server.URL, "--receiver-id", n.ReceiverID)
		if err := Run(ctx, args, &out, &diag); err != nil {
			t.Fatal(args, err, diag.String())
		}
		return out.Bytes()
	}
	var empty paperstrategy.Catalog
	if err := json.Unmarshal(run("paper-strategies"), &empty); err != nil || empty.Validate() != nil || len(empty.Records) != 0 {
		t.Fatal(empty, err)
	}
	args := []string{"paper-strategy-register", "--file", file}
	var first, retry paperstrategy.Receipt
	if err := json.Unmarshal(run(args...), &first); err != nil || first.Record.Validate() != nil || first.Duplicate || first.Record.Command.Definition != d {
		t.Fatal(first, err)
	}
	if err := json.Unmarshal(run(args...), &retry); err != nil || !retry.Duplicate || !reflect.DeepEqual(first.Record, retry.Record) {
		t.Fatal(retry, err)
	}
	var selected paperstrategy.Record
	if err := json.Unmarshal(run("paper-strategy", "--id", d.ID, "--record-sha256", first.Record.SHA256), &selected); err != nil || !reflect.DeepEqual(selected, first.Record) {
		t.Fatal(selected, err)
	}
	if err := json.Unmarshal(run("paper-strategies"), &empty); err != nil || empty.Validate() != nil || len(empty.Records) != 1 {
		t.Fatal(empty, err)
	}
	body, _ := json.Marshal(first.Record.Command)
	for _, tc := range []struct {
		method, path, body, token string
		want                      int
	}{
		{"GET", "/v1/paper-strategies", "", "", 401},
		{"POST", "/v1/paper-strategies", string(body), "", 401},
		{"GET", "/v1/paper-strategies?", "", "strategy-test-token", 422},
		{"GET", "/v1/paper-strategies?%zz", "", "strategy-test-token", 422},
		{"POST", "/v1/paper-strategies?account=paper", string(body), "strategy-test-token", 422},
		{"GET", "/v1/paper-strategies/missing", "", "strategy-test-token", 404},
		{"POST", "/v1/paper-strategies/baseline/activate", "{}", "strategy-test-token", 404},
		{"POST", "/v1/paper-strategies", strings.Replace(string(body), `"definition":`, `"account_id":"paper","definition":`, 1), "strategy-test-token", 422},
		{"POST", "/v1/paper-strategies", strings.Replace(string(body), `"definition":`, `"admission":true,"definition":`, 1), "strategy-test-token", 422},
		{"POST", "/v1/paper-strategies", strings.Replace(string(body), `"receiver_id":`, `"receiver_id":"duplicate","receiver_id":`, 1), "strategy-test-token", 422},
		{"POST", "/v1/paper-strategies", strings.Replace(string(body), `"receiver_id":`, `"Receiver_id":`, 1), "strategy-test-token", 422},
		{"POST", "/v1/paper-strategies", string(body) + "{}", "strategy-test-token", 422},
		{"POST", "/v1/paper-strategies", "null", "strategy-test-token", 422},
		{"POST", "/v1/paper-strategies", strings.Repeat(" ", paperstrategy.MaxCommandBytes+1), "strategy-test-token", 422},
		{"POST", "/v1/paper-strategies", strings.Replace(string(body), `"quantity":10`, `"quantity":9`, 1), "strategy-test-token", 409},
		{"POST", "/v1/paper-strategies", strings.Replace(string(body), n.ReceiverID, "receiver-other_owner_00000001", 1), "strategy-test-token", 409},
	} {
		req, _ := http.NewRequestWithContext(ctx, tc.method, server.URL+tc.path, strings.NewReader(tc.body))
		if tc.token != "" {
			req.Header.Set("Authorization", "Bearer "+tc.token)
		}
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(res.Body)
		res.Body.Close()
		if res.StatusCode != tc.want {
			t.Fatal(tc.path, res.StatusCode, tc.want, string(b))
		}
	}
	for _, args := range [][]string{{"paper-strategy", "--id", d.ID, "--record-sha256", strings.Repeat("f", 64)}, {"paper-strategies", "--receiver-id", "receiver-other_owner_00000001"}, {"paper-strategy-register", "--file", file, "--account-id", "paper"}} {
		var out, diag bytes.Buffer
		args = append([]string{args[0], "--api-url", server.URL, "--receiver-id", n.ReceiverID}, args[1:]...)
		if err := Run(ctx, args, &out, &diag); err == nil || out.Len() != 0 {
			t.Fatal("unsafe client emitted output", args, err)
		}
	}
	after, _ := s.Snapshot(ctx)
	if !reflect.DeepEqual(before, after) {
		t.Fatal("configuration changed operational state")
	}
}
func TestPaperStrategyClientRejectsSubstitutedOrAmbiguousResponses(t *testing.T) {
	d := strategyHTTPDefinition()
	owner := "receiver-paperstrategy_cli_01"
	r, err := paperstrategy.New(paperstrategy.Command{ReceiverID: owner, Definition: d}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(t.TempDir(), "definition.json")
	raw, _ := json.Marshal(d)
	if err := os.WriteFile(file, raw, 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("MANTIK_API_TOKEN", "strategy-test-token")
	for _, kind := range []string{"unknown-field", "substituted-command", "admission-flag", "wrong-receiver"} {
		t.Run(kind, func(t *testing.T) {
			v := r
			if kind == "substituted-command" {
				v.Command.Definition.Quantity--
				v, _ = paperstrategy.New(v.Command, v.RegisteredAt)
			}
			if kind == "wrong-receiver" {
				v.Command.ReceiverID = "receiver-other_owner_00000001"
				v, _ = paperstrategy.New(v.Command, v.RegisteredAt)
			}
			if kind == "admission-flag" {
				v.AdmissionAvailable = true
			}
			payload, _ := json.Marshal(paperstrategy.Receipt{Record: v})
			if kind == "unknown-field" {
				payload = append([]byte(`{"unexpected":true,`), payload[1:]...)
			}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write(payload) }))
			defer server.Close()
			var out, diag bytes.Buffer
			if err := Run(context.Background(), []string{"paper-strategy-register", "--api-url", server.URL, "--receiver-id", owner, "--file", file}, &out, &diag); err == nil || out.Len() != 0 {
				t.Fatal("hostile response emitted", err)
			}
		})
	}
}
