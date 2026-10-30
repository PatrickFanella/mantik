package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"git.subcult.tv/subculture-collective/mantik/internal/loopbacktest"
	"git.subcult.tv/subculture-collective/mantik/internal/paperaccount"
	"git.subcult.tv/subculture-collective/mantik/internal/paperledger"
	"git.subcult.tv/subculture-collective/mantik/internal/service"
	"git.subcult.tv/subculture-collective/mantik/internal/storage"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

type rehearsalClock struct{ at time.Time }

func (c rehearsalClock) Now() time.Time                                  { return c.at }
func (c rehearsalClock) Wait(ctx context.Context, _ time.Duration) error { return ctx.Err() }
func TestPaperLedgerRehearsalCLIAndHTTP(t *testing.T) {
	ctx := context.Background()
	s := storage.NewMemory()
	receiver, _ := s.ResearchSnapshot(ctx)
	at := time.Unix(100, 0).UTC()
	a, err := s.CreatePaperAccount(ctx, receiver.ID, paperaccount.Config{ID: "pilot", Currency: "USD", OpeningCashCents: 100000, CapitalLimitCents: 60000}, func() time.Time { return at })
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(loopbacktest.GoClientOnly(service.HandlerWithClock(s, "ledger-token", rehearsalClock{at.Add(time.Hour)})))
	defer server.Close()
	t.Setenv("MANTIK_API_TOKEN", "ledger-token")
	run := func(args ...string) []byte {
		t.Helper()
		var stdout, stderr bytes.Buffer
		args = append(args, "--api-url", server.URL)
		if err := Run(ctx, args, &stdout, &stderr); err != nil {
			t.Fatal(err, stderr.String())
		}
		return stdout.Bytes()
	}
	var genesis paperledger.Rehearsal
	_ = json.Unmarshal(run("paper-ledger-rehearsal", "--account", "pilot"), &genesis)
	if genesis.Validate(ctx) != nil || len(genesis.Accepted) != 0 {
		t.Fatal(genesis)
	}
	cmd := paperledger.AppendCommand{ReceiverID: receiver.ID, AccountSHA256: a.Account.SHA256, ExpectedHeadSHA256: genesis.Result.LedgerHeadSHA256, ScopeCode: paperledger.RehearsalScope, Entry: paperledger.Entry{ID: "reserve", Kind: "reserve", ReservationID: "one", AmountCents: 40000, OccurredAt: at}}
	file := filepath.Join(t.TempDir(), "command.json")
	raw, _ := json.Marshal(cmd)
	_ = os.WriteFile(file, raw, 0600)
	var first, retry paperledger.AppendReceipt
	_ = json.Unmarshal(run("paper-ledger-record", "--account", "pilot", "--file", file), &first)
	_ = json.Unmarshal(run("paper-ledger-record", "--account", "pilot", "--file", file), &retry)
	if first.Validate("pilot", cmd) != nil || first.Duplicate || !retry.Duplicate || !reflect.DeepEqual(first.Accepted, retry.Accepted) {
		t.Fatal(first, retry)
	}
	var current paperledger.Rehearsal
	_ = json.Unmarshal(run("paper-ledger-rehearsal", "--account", "pilot"), &current)
	if current.Validate(ctx) != nil || len(current.Accepted) != 1 || current.Result.ReservedCashCents != 40000 {
		t.Fatal(current)
	}
	call := func(method, path, body string, auth bool, want int) {
		t.Helper()
		req, _ := http.NewRequestWithContext(ctx, method, server.URL+path, strings.NewReader(body))
		if auth {
			req.Header.Set("Authorization", "Bearer ledger-token")
		}
		response, err := server.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		if response.StatusCode != want {
			t.Fatal(method, path, response.StatusCode, want)
		}
	}
	path := "/v1/paper-accounts/pilot/ledger-rehearsal"
	call("GET", path, "", false, 401)
	call("GET", path+"?limit=1", "", true, 422)
	call("GET", "/v1/paper-accounts/missing/ledger-rehearsal", "", true, 404)
	call("POST", path+"/entries", string(raw), false, 401)
	changed := cmd
	changed.Entry.AmountCents--
	changedRaw, _ := json.Marshal(changed)
	call("POST", path+"/entries", string(changedRaw), true, 409)
	call("POST", path+"/entries", strings.Replace(string(raw), `"scope_code":"`+paperledger.RehearsalScope+`"`, `"scope_code":"execute"`, 1), true, 422)
	call("POST", path+"/entries", strings.Replace(string(raw), `"entry":`, `"extra":true,"entry":`, 1), true, 400)
	call("POST", path+"/entries", strings.Replace(string(raw), `"scope_code":`, `"scope_code":"execute","scope_code":`, 1), true, 400)
	call("POST", path+"/entries", `{"schema":"`+paperledger.RecordingSchema+`","entries":[]}`, true, 400)
	for _, args := range [][]string{{"paper-ledger-record", "--account", "pilot"}, {"paper-ledger-rehearsal"}, {"paper-ledger-rehearsal", "--account", "../bad"}, {"paper-ledger-record", "--account", "pilot", "--file", file, "extra"}} {
		var out, errOut bytes.Buffer
		if err := Run(ctx, args, &out, &errOut); err == nil || out.Len() != 0 {
			t.Fatal(args, err)
		}
	}
	after, _ := s.PaperLedgerRehearsal(ctx, "pilot")
	accounts, _ := s.PaperAccounts(ctx)
	if !reflect.DeepEqual(current, after) || len(accounts.Accounts) != 1 || accounts.Accounts[0] != a.Account {
		t.Fatal("rejected writes changed state")
	}
}
func TestRehearsalClientRefusesInvalidReadback(t *testing.T) {
	server := httptest.NewServer(loopbacktest.GoClientOnly(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"schema":"mantik-paper-ledger-rehearsal-v1","qualification_credit":true}`))
	})))
	defer server.Close()
	var out bytes.Buffer
	if err := Run(context.Background(), []string{"paper-ledger-rehearsal", "--account", "pilot", "--api-url", server.URL}, &out, &out); err == nil || out.Len() != 0 {
		t.Fatal(err)
	}
}
