package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"git.subcult.tv/subculture-collective/mantik/internal/loopbacktest"
	"git.subcult.tv/subculture-collective/mantik/internal/paperaccount"
	"git.subcult.tv/subculture-collective/mantik/internal/service"
	"git.subcult.tv/subculture-collective/mantik/internal/storage"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestPaperAccountCLIAndHTTPContracts(t *testing.T) {
	ctx := context.Background()
	s := storage.NewMemory()
	receiver, _ := s.ResearchSnapshot(ctx)
	server := httptest.NewServer(loopbacktest.GoClientOnly(service.Handler(s, "paper-account-token")))
	defer server.Close()
	t.Setenv("MANTIK_API_TOKEN", "paper-account-token")
	run := func(args ...string) []byte {
		t.Helper()
		args = append(args, "--api-url", server.URL)
		var out bytes.Buffer
		if err := Run(ctx, args, &out, &out); err != nil {
			t.Fatal(err, out.String())
		}
		return out.Bytes()
	}
	var empty paperaccount.Catalog
	if err := json.Unmarshal(run("paper-accounts"), &empty); err != nil || empty.Validate() != nil || len(empty.Accounts) != 0 {
		t.Fatal(err)
	}
	args := []string{"paper-account-create", "--id", "pilot", "--receiver-id", receiver.ID, "--opening-cash-cents", "10000", "--capital-limit-cents", "5000"}
	var first, retry paperaccount.Receipt
	_ = json.Unmarshal(run(args...), &first)
	_ = json.Unmarshal(run(args...), &retry)
	if first.Account.Validate() != nil || first.Duplicate || !retry.Duplicate || first.Account != retry.Account {
		t.Fatal(first, retry)
	}
	var one paperaccount.Account
	_ = json.Unmarshal(run("paper-account", "--id", "pilot"), &one)
	if one != first.Account {
		t.Fatal(one)
	}
	var all paperaccount.Catalog
	_ = json.Unmarshal(run("paper-accounts"), &all)
	if all.Validate() != nil || len(all.Accounts) != 1 || all.Accounts[0] != one {
		t.Fatal(all)
	}
	for _, args := range [][]string{{"paper-account-create"}, {"paper-account", "--id", "../bad"}, {"paper-accounts", "--id", "pilot"}, {"paper-account-create", "--id", "pilot", "--receiver-id", receiver.ID, "--opening-cash-cents", "1", "--capital-limit-cents", "2"}, {"paper-accounts", "extra"}} {
		var out bytes.Buffer
		if err := Run(ctx, append(args, "--api-url", server.URL), &out, &out); err == nil {
			t.Fatal("invalid command accepted", args)
		}
	}
	call := func(method, path, body string, auth bool, want int) {
		t.Helper()
		req, _ := http.NewRequestWithContext(ctx, method, server.URL+path, strings.NewReader(body))
		if auth {
			req.Header.Set("Authorization", "Bearer paper-account-token")
		}
		res, err := server.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		if res.StatusCode != want {
			t.Fatal(method, path, res.StatusCode, want)
		}
	}
	call("GET", "/v1/paper-accounts", "", false, 401)
	call("GET", "/v1/paper-accounts?limit=1", "", true, 422)
	call("GET", "/v1/paper-accounts/missing", "", true, 404)
	call("POST", "/v1/paper-accounts", `{"receiver_id":"`+receiver.ID+`","config":{"id":"pilot","currency":"USD","opening_cash_cents":10000,"capital_limit_cents":4000}}`, true, 409)
	call("POST", "/v1/paper-accounts", `{"receiver_id":"receiver-other_other_other","config":{"id":"other","currency":"USD","opening_cash_cents":100,"capital_limit_cents":50}}`, true, 409)
	for _, body := range []string{`{}`, `{"account":"real"}`, `{"receiver_id":"` + receiver.ID + `","config":{"id":"another","currency":"USD","opening_cash_cents":100.5,"capital_limit_cents":50}}`, `{"receiver_id":"` + receiver.ID + `","config":{"id":"another","currency":"USD","opening_cash_cents":100,"capital_limit_cents":50,"broker_id":"forbidden"}}`} {
		req, _ := http.NewRequestWithContext(ctx, "POST", server.URL+"/v1/paper-accounts", strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer paper-account-token")
		res, err := server.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		if res.StatusCode < 400 {
			t.Fatal("invalid HTTP body admitted", body)
		}
	}
	after, _ := s.PaperAccounts(ctx)
	if len(after.Accounts) != 1 || after.Accounts[0] != one {
		t.Fatal("rejected writes changed account")
	}
}
func TestPaperAccountClientRejectsForgedReadbacks(t *testing.T) {
	server := httptest.NewServer(loopbacktest.GoClientOnly(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"schema":"mantik-paper-account-catalog-v1","receiver_id":"receiver-abcdefghijklmnop","accounts":null}`))
	})))
	defer server.Close()
	var out bytes.Buffer
	if err := Run(context.Background(), []string{"paper-accounts", "--api-url", server.URL}, &out, &out); err == nil || out.Len() != 0 {
		t.Fatal("invalid catalogue emitted", err)
	}
}
