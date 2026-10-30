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

	"git.subcult.tv/subculture-collective/mantik/internal/allocation"
	"git.subcult.tv/subculture-collective/mantik/internal/loopbacktest"
	"git.subcult.tv/subculture-collective/mantik/internal/paperaccount"
	"git.subcult.tv/subculture-collective/mantik/internal/service"
	"git.subcult.tv/subculture-collective/mantik/internal/storage"
)

type policyHTTPBackend interface {
	storage.Store
	storage.PaperAccountStore
	storage.ResearchReceiverStore
	storage.AllocationPolicyStore
}

func TestAllocationPolicyCLIHTTP(t *testing.T) { allocationPolicyHTTP(t, storage.NewMemory()) }

// The existing disposable CLI/PostgreSQL contract also calls this helper.
func allocationPolicyHTTP(t *testing.T, s policyHTTPBackend) {
	t.Helper()
	ctx := context.Background()
	receiver, err := s.ResearchSnapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	a, err := s.CreatePaperAccount(ctx, receiver.ID, paperaccount.Config{ID: "policy-account", Currency: "USD", OpeningCashCents: 100000, CapitalLimitCents: 50000}, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	policy := allocation.Policy{ID: "policy-v1", AccountSHA256: a.Account.SHA256, MaxSnapshotAgeSeconds: 60, Strategies: []allocation.Strategy{{ID: "strategy-v1", ArtifactSHA256: strings.Repeat("a", 64), LimitCents: 40000}}, Symbols: []allocation.Symbol{{ID: "DEMO", LimitCents: 40000, Groups: []string{}}}, Groups: []allocation.Group{}}
	file := filepath.Join(t.TempDir(), "policy.json")
	raw, _ := json.Marshal(policy)
	if err = os.WriteFile(file, raw, 0600); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(loopbacktest.GoClientOnly(service.Handler(s, "policy-token")))
	defer server.Close()
	t.Setenv("MANTIK_API_TOKEN", "policy-token")
	run := func(args ...string) []byte {
		t.Helper()
		var out, diag bytes.Buffer
		args = append(args, "--api-url", server.URL, "--receiver-id", receiver.ID)
		if err := Run(ctx, args, &out, &diag); err != nil {
			t.Fatal(args, err, diag.String())
		}
		return out.Bytes()
	}
	var empty allocation.PolicyCatalog
	if err = json.Unmarshal(run("allocation-policies"), &empty); err != nil || empty.Validate() != nil || len(empty.Records) != 0 {
		t.Fatal(err)
	}
	args := []string{"allocation-policy-register", "--account-id", a.Account.Config.ID, "--file", file}
	var first, retry allocation.PolicyReceipt
	if err = json.Unmarshal(run(args...), &first); err != nil || first.Record.Validate() != nil || first.Duplicate {
		t.Fatal(first, err)
	}
	if err = json.Unmarshal(run(args...), &retry); err != nil || !retry.Duplicate || !reflect.DeepEqual(first.Record, retry.Record) {
		t.Fatal(retry, err)
	}
	var selected allocation.PolicyRecord
	if err = json.Unmarshal(run("allocation-policy", "--id", policy.ID, "--record-sha256", first.Record.SHA256), &selected); err != nil || !reflect.DeepEqual(selected, first.Record) {
		t.Fatal(err)
	}
	var catalog allocation.PolicyCatalog
	if err = json.Unmarshal(run("allocation-policies"), &catalog); err != nil || catalog.Validate() != nil || len(catalog.Records) != 1 || !reflect.DeepEqual(catalog.Records[0], selected) {
		t.Fatal(err)
	}
	body, _ := json.Marshal(first.Record.Command)
	for _, tc := range []struct {
		method, path, body, token string
		code                      int
	}{
		{"GET", "/v1/allocation-policies", "", "", 401},
		{"GET", "/v1/allocation-policies?", "", "policy-token", 422},
		{"GET", "/v1/allocation-policies?%zz", "", "policy-token", 422},
		{"POST", "/v1/allocation-policies?x=1", string(body), "policy-token", 422},
		{"GET", "/v1/allocation-policies/missing", "", "policy-token", 404},
		{"POST", "/v1/allocation-policies", strings.Replace(string(body), `"policy":`, `"balance":1,"policy":`, 1), "policy-token", 422},
		{"POST", "/v1/allocation-policies", strings.Replace(string(body), `"policy":`, `"admission":{},"policy":`, 1), "policy-token", 422},
		{"POST", "/v1/allocation-policies", strings.Replace(string(body), `"policy-v1"`, `"replacement"`, 1), "policy-token", 409},
		{"POST", "/v1/allocation-policies", string(body) + string(body), "policy-token", 422},
		{"POST", "/v1/allocation-policies", "null", "policy-token", 422},
		{"POST", "/v1/allocation-policies", strings.Repeat(" ", allocation.MaxPolicyRecordBytes+1), "policy-token", 422},
	} {
		req, _ := http.NewRequest(tc.method, server.URL+tc.path, strings.NewReader(tc.body))
		if tc.token != "" {
			req.Header.Set("Authorization", "Bearer "+tc.token)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		payload, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != tc.code {
			t.Fatalf("%s %s: %d %s", tc.method, tc.path, resp.StatusCode, payload)
		}
	}
	// Hostile responses may be structurally valid but belong to another command
	// or receiver; clients must reject before emitting any bytes.
	wrong := first.Record
	wrong.Command.Policy.ID = "other"
	wrong, _ = allocation.NewPolicyRecord(wrong.Command, wrong.Account, wrong.RegisteredAt)
	for _, payload := range []any{allocation.PolicyReceipt{Record: wrong}, map[string]any{"record": first.Record, "duplicate": false, "extra": true}} {
		host := httptest.NewServer(loopbacktest.GoClientOnly(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _ = json.NewEncoder(w).Encode(payload) })))
		var out, diag bytes.Buffer
		err := Run(ctx, append(args, "--api-url", host.URL, "--receiver-id", receiver.ID), &out, &diag)
		host.Close()
		if err == nil || out.Len() != 0 {
			t.Fatal("accepted substituted response", err, out.String())
		}
	}
	for _, args := range [][]string{{"allocation-policy", "--receiver-id", receiver.ID, "--id", policy.ID, "--record-sha256", strings.Repeat("f", 64)}, {"allocation-policies", "--receiver-id", "receiver-other_other_other"}, {"allocation-policy-register", "--receiver-id", receiver.ID, "--account-id", a.Account.Config.ID, "--file", file, "extra"}} {
		var out, diag bytes.Buffer
		err := Run(ctx, append(args, "--api-url", server.URL), &out, &diag)
		if err == nil || out.Len() != 0 {
			t.Fatal(args, err)
		}
	}
	for _, cmd := range []string{"allocation-policy-register", "allocation-policy", "allocation-policies"} {
		var out, diag bytes.Buffer
		if err = Run(ctx, []string{cmd, "--help"}, &out, &diag); err != nil || !strings.Contains(diag.String(), "receiver-id") {
			t.Fatal(cmd, err)
		}
	}
}
