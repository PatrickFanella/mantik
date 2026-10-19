package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/paperaccount"
	"git.subcult.tv/subculture-collective/mantik/internal/service"
	"git.subcult.tv/subculture-collective/mantik/internal/storage"
	"git.subcult.tv/subculture-collective/mantik/internal/workflow"
)

func TestFixtureCapitalPublicActivation(t *testing.T) {
	ctx := context.Background()
	s := storage.NewQueuedMemory()
	receiver, _ := s.ResearchSnapshot(ctx)
	server := httptest.NewServer(service.HandlerWithClock(s, "capital-token", rehearsalClock{workflow.Fixtures()[0].ReceivedAt}))
	defer server.Close()
	t.Setenv("MANTIK_API_TOKEN", "capital-token")
	run := func(ok bool, args ...string) []byte {
		t.Helper()
		var out, stderr bytes.Buffer
		err := Run(ctx, append(args, "--api-url", server.URL), &out, &stderr)
		if ok && err != nil || !ok && (err == nil || out.Len() != 0) {
			t.Fatal(args, err, stderr.String())
		}
		return out.Bytes()
	}
	var funding paperaccount.Receipt
	if err := json.Unmarshal(run(true, "paper-account-create", "--id", "pilot", "--receiver-id", receiver.ID, "--opening-cash-cents", "100000", "--capital-limit-cents", "100000"), &funding); err != nil {
		t.Fatal(err)
	}
	c := storage.FixtureCapitalBinding{ReceiverID: receiver.ID, AccountID: "pilot", AccountSHA256: funding.Account.SHA256, ScopeCode: storage.FixtureCapitalBindingScope}
	raw, _ := json.Marshal(c)
	call := func(method, path, body string, auth bool, want int) {
		t.Helper()
		req, _ := http.NewRequestWithContext(ctx, method, server.URL+path, strings.NewReader(body))
		if auth {
			req.Header.Set("Authorization", "Bearer capital-token")
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
	call("GET", "/v1/fixture-capital", "", true, 404)
	call("GET", "/v1/fixture-capital", "", false, 401)
	call("POST", "/v1/fixture-capital/binding", string(raw), false, 401)
	call("GET", "/v1/fixture-capital?limit=1", "", true, 422)
	for _, body := range []string{
		`{"receiver_id":`, string(raw) + ` {}`, strings.Replace(string(raw), `"scope_code":`, `"extra":true,"scope_code":`, 1), strings.Replace(string(raw), `"scope_code":`, `"scope_code":"live","scope_code":`, 1), strings.Repeat(" ", 16<<10) + string(raw), `{"capital":{},"worker_bound":true}`,
	} {
		call("POST", "/v1/fixture-capital/binding", body, true, 400)
	}
	call("POST", "/v1/fixture-capital/binding", `{}`, true, 422)
	call("POST", "/v1/fixture-capital/binding", strings.Replace(string(raw), storage.FixtureCapitalBindingScope, "execute", 1), true, 422)
	call("POST", "/v1/fixture-capital/binding", strings.Replace(string(raw), receiver.ID, "receiver-other_other_other", 1), true, 409)
	call("POST", "/v1/fixture-capital/binding", strings.Replace(string(raw), funding.Account.SHA256, strings.Repeat("0", 64), 1), true, 409)
	call("POST", "/v1/fixture-capital/binding", strings.Replace(string(raw), `"pilot"`, `"missing"`, 1), true, 404)
	if _, err := s.FixtureCapital(ctx); err == nil {
		t.Fatal("rejected request activated account")
	}
	bindArgs := []string{"fixture-capital-bind", "--account", "pilot", "--receiver-id", receiver.ID, "--account-sha256", funding.Account.SHA256, "--scope-code", storage.FixtureCapitalBindingScope}
	var first, retry storage.FixtureCapitalReadback
	_ = json.Unmarshal(run(true, bindArgs...), &first)
	_ = json.Unmarshal(run(true, bindArgs...), &retry)
	if first.Validate(funding.Account) != nil || !reflect.DeepEqual(first, retry) {
		t.Fatal("binding retry", first, retry)
	}
	for _, q := range []int{1, 3} {
		quote := workflow.Fixtures()[q]
		if _, err := s.Ingest(ctx, quote); err != nil {
			t.Fatal(err)
		}
		if err := service.RunActionCycle(ctx, s, "public-worker", rehearsalClock{quote.ReceivedAt}, time.Second*30); err != nil {
			t.Fatal(err)
		}
	}
	var closed storage.FixtureCapitalReadback
	_ = json.Unmarshal(run(true, "fixture-capital"), &closed)
	if closed.Validate(funding.Account) != nil || closed.Capital.AvailableCashCents != 94000 || closed.Capital.RealizedPnLCents != -6000 || closed.Capital.Position != nil {
		t.Fatal(closed)
	}
	_ = json.Unmarshal(run(true, bindArgs...), &retry)
	if !reflect.DeepEqual(closed, retry) {
		t.Fatal("binding retry reset closed capital")
	}
	for _, args := range [][]string{{"fixture-capital-bind"}, {"fixture-capital", "extra"}, {"fixture-capital", "--account", "pilot"}, bindArgs[:len(bindArgs)-2], {"fixture-capital-bind", "--account", "../bad"}} {
		run(false, args...)
	}
	after, _ := s.PaperAccounts(ctx)
	if len(after.Accounts) != 1 || after.Accounts[0] != funding.Account {
		t.Fatal("activation rewrote opening funding")
	}
}

func TestFixtureCapitalCannotAdoptActiveCase(t *testing.T) {
	ctx := context.Background()
	s := storage.NewQueuedMemory()
	receiver, _ := s.ResearchSnapshot(ctx)
	r, err := s.CreatePaperAccount(ctx, receiver.ID, paperaccount.Config{ID: "late", Currency: "USD", OpeningCashCents: 100000, CapitalLimitCents: 100000}, func() time.Time { return workflow.Fixtures()[0].ReceivedAt })
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Ingest(ctx, workflow.Fixtures()[1]); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(service.Handler(s, "late-token"))
	defer server.Close()
	t.Setenv("MANTIK_API_TOKEN", "late-token")
	var out, stderr bytes.Buffer
	err = Run(ctx, []string{"fixture-capital-bind", "--account", "late", "--receiver-id", receiver.ID, "--account-sha256", r.Account.SHA256, "--scope-code", storage.FixtureCapitalBindingScope, "--api-url", server.URL}, &out, &stderr)
	if err == nil || out.Len() != 0 {
		t.Fatal("adopted active fixture", err)
	}
	if _, err := s.FixtureCapital(ctx); err == nil {
		t.Fatal("failed activation left a binding")
	}
}

func TestFixtureCapitalClientChecksFundingAndReadback(t *testing.T) {
	ctx := context.Background()
	s := storage.NewQueuedMemory()
	receiver, _ := s.ResearchSnapshot(ctx)
	r, err := s.CreatePaperAccount(ctx, receiver.ID, paperaccount.Config{ID: "pilot", Currency: "USD", OpeningCashCents: 100000, CapitalLimitCents: 100000}, func() time.Time { return workflow.Fixtures()[0].ReceivedAt })
	if err != nil {
		t.Fatal(err)
	}
	v, err := s.BindFixtureCapital(ctx, storage.FixtureCapitalBinding{ReceiverID: receiver.ID, AccountID: "pilot", AccountSHA256: r.Account.SHA256, ScopeCode: storage.FixtureCapitalBindingScope})
	if err != nil {
		t.Fatal(err)
	}
	for _, mutate := range []func(*storage.FixtureCapitalReadback, *paperaccount.Account){
		func(v *storage.FixtureCapitalReadback, a *paperaccount.Account) { v.Capital.AvailableCashCents++ },
		func(v *storage.FixtureCapitalReadback, a *paperaccount.Account) { v.QualificationCredit = true },
		func(v *storage.FixtureCapitalReadback, a *paperaccount.Account) { v.Capital.Commitments = nil },
		func(v *storage.FixtureCapitalReadback, a *paperaccount.Account) { a.Config.CapitalLimitCents-- },
		func(v *storage.FixtureCapitalReadback, a *paperaccount.Account) { v.Capital.AccountID = "../elsewhere" },
	} {
		bad, a := v, r.Account
		mutate(&bad, &a)
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			if req.URL.Path == "/v1/fixture-capital" {
				_ = json.NewEncoder(w).Encode(bad)
			} else {
				_ = json.NewEncoder(w).Encode(a)
			}
		}))
		var out, stderr bytes.Buffer
		err := Run(ctx, []string{"fixture-capital", "--api-url", server.URL}, &out, &stderr)
		server.Close()
		if err == nil || out.Len() != 0 {
			t.Fatal("invalid readback emitted", err)
		}
	}
}
