package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/account"
	"git.subcult.tv/subculture-collective/mantik/internal/allocation"
	"git.subcult.tv/subculture-collective/mantik/internal/loopbacktest"
	"git.subcult.tv/subculture-collective/mantik/internal/service"
	"git.subcult.tv/subculture-collective/mantik/internal/storage"
)

func TestOwnerBookTerminalWalkthrough(t *testing.T) {
	ctx := context.Background()
	s := storage.NewMemory()
	at := time.Now().UTC().Add(-time.Second)
	cfg := account.DefaultCappedConfig("owner-walkthrough", "isolated-paper", account.Cash, at.AddDate(0, 0, -6))
	cfg.FundingMode = account.OwnerReceipts
	b, err := s.CreateCappedBook(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	b, err = s.ApplyCappedBook(ctx, cfg.ID, b.Version, account.BookCommand{ID: "fixture-protective-halt", Kind: "halt", At: at})
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("MANTIK_API_TOKEN", "isolated-owner-test-token")
	server := httptest.NewServer(loopbacktest.GoClientOnly(service.Handler(s, "isolated-owner-test-token")))
	defer server.Close()
	run := func(args []string, input string) (string, error) {
		t.Helper()
		var out, errOut bytes.Buffer
		err := runOwnerBook(ctx, append([]string{"--api-url", server.URL, "--id", cfg.ID}, args...), strings.NewReader(input), &out, &errOut)
		t.Log(out.String())
		return out.String(), err
	}
	if out, err := run(nil, ""); err != nil || !strings.Contains(out, "halted=true") || !strings.Contains(out, "Equity=2000.000000000") {
		t.Fatal(out, err)
	}
	clear := account.OwnerBookAction{ExpectedVersion: b.Version, Action: "owner_clear_halt"}
	clear.Confirmation = clear.ConfirmationPin(cfg.ID, "owner-clear-1")
	args := []string{"--action", clear.Action, "--action-id", "owner-clear-1", "--expected-version", "1"}
	if _, err = run(args, clear.Confirmation+"\n"); err != nil {
		t.Fatal(err)
	}
	if out, err := run(append(args, "--confirm", clear.Confirmation), ""); err != nil || !strings.Contains(out, "applied_version=2") || !strings.Contains(out, "halted=false") {
		t.Fatal(out, err)
	}
	due, _ := cfg.DueDeposits(at)
	deposit := account.OwnerBookAction{ExpectedVersion: 2, Action: "deposit", Amount: "500", Source: "isolated-fixture-receipt-no-real-cash", EffectiveAt: due[0].EffectiveAt.Add(time.Hour)}
	deposit.Confirmation = deposit.ConfirmationPin(cfg.ID, due[0].ID)
	args = []string{"--action", "deposit", "--action-id", due[0].ID, "--expected-version", "2", "--amount", "500", "--source", deposit.Source, "--effective-at", deposit.EffectiveAt.Format(time.RFC3339Nano), "--confirm", deposit.Confirmation}
	if out, err := run(args, ""); err != nil || !strings.Contains(out, "Cash=2500.000000000") || !strings.Contains(out, "owner_recorded_real=true") {
		t.Fatal(out, err)
	}
	after, _ := s.CappedBook(ctx, cfg.ID)
	if after.Version != 3 {
		t.Fatal(after.Version)
	}
}

func TestOwnerBookFillReadbackAndCashRefusal(t *testing.T) {
	for name, activity := range map[string]string{"punctuation": "activity:123", "maximum length": strings.Repeat("a", 128), "terminal controls": "activity:\x1b[31m\noriginal"} {
		t.Run(name, func(t *testing.T) { ownerBookFillReadbackContract(t, activity) })
	}
}

func ownerBookFillReadbackContract(t *testing.T, activity string) {
	t.Helper()
	at := time.Date(2026, 11, 16, 15, 0, 0, 0, time.UTC)
	s, err := account.NewCappedState(account.DefaultCappedConfig("fill-view", "isolated", account.Cash, at))
	if err != nil {
		t.Fatal(err)
	}
	order := account.BookOrder{ID: "buy", Symbol: "SPY", Quantity: "0.125", Limit: "100", Fee: "0.02", Side: "buy", At: at}
	fill := account.BookFill{ID: activity, OrderID: order.ID, VenueOrderID: "venue", Symbol: "SPY", Quantity: order.Quantity, Price: "100", Fee: "0.01", Side: "buy", SourceSHA256: strings.Repeat("a", 64), At: at.Add(time.Second)}
	for _, c := range []account.BookCommand{
		{ID: order.ID, Kind: "reserve", At: at, Order: &order, Marks: map[string]string{"SPY": "100"}},
		{ID: "bind", Kind: "bind", At: at, OrderID: order.ID, VenueOrderID: "venue"},
		{ID: fill.ID, Kind: "fill", At: at.Add(2 * time.Second), Fill: &fill, Marks: map[string]string{"SPY": "100"}},
	} {
		s, err = account.ApplyCappedCommand(s, c)
		if err != nil {
			t.Fatal(err)
		}
	}
	v, err := account.ReadOwnerBook(s, at.Add(3*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := outputOwnerBook(&out, v, "terminal"); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{fmt.Sprintf("fill=%q", activity), `quantity="0.125"`, `gross="12.5"`, `fee="0.01"`, `cash_delta="-12.51"`, `reconciled_cash="1987.49"`, `source_sha256="` + fill.SourceSHA256 + `"`, "economic_at=2026-11-16T15:00:01Z recorded_at=2026-11-16T15:00:02Z"} {
		if !strings.Contains(out.String(), want) {
			t.Fatal("terminal omitted retained economics", want, out.String())
		}
	}
	if strings.Contains(out.String(), "\x1b[31m") {
		t.Fatal("raw activity ID executed terminal control bytes")
	}
	out.Reset()
	if err := outputOwnerBook(&out, v, "json"); err != nil {
		t.Fatal(err)
	}
	var decoded account.OwnerBookView
	if err := json.Unmarshal(out.Bytes(), &decoded); err != nil || decoded.Execution == nil || decoded.Execution.Fills[0].Fill.ID != activity {
		t.Fatal("JSON changed original activity identity", err)
	}
	v.Execution.ReconciledCash = "2000"
	for _, format := range []string{"terminal", "json"} {
		out.Reset()
		if err := outputOwnerBook(&out, v, format); err == nil || out.Len() != 0 {
			t.Fatal("inconsistent cash produced successful output", format, err)
		}
	}
}

func TestOwnerBookCancellationNeverSendsMutation(t *testing.T) {
	t.Setenv("MANTIK_API_TOKEN", "test-owner")
	calls := 0
	server := httptest.NewServer(loopbacktest.GoClientOnly(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls++; http.Error(w, "unexpected", 500) })))
	defer server.Close()
	a := account.OwnerBookAction{Action: "pause_strategy", StrategyID: "strategy-1", ExpectedVersion: 5}
	pin := a.ConfirmationPin("book", "pause-1")
	base := []string{"--api-url", server.URL, "--id", "book", "--action", a.Action, "--strategy-id", a.StrategyID, "--action-id", "pause-1", "--expected-version", "5"}
	for _, tc := range []struct {
		name, input string
		extra       []string
		wantError   bool
	}{
		{name: "EOF", wantError: true},
		{name: "pin-at-EOF", input: pin, wantError: true},
		{name: "yes", input: "yes\n", wantError: true},
		{name: "incorrect-pin", extra: []string{"--confirm", "yes"}, wantError: true},
		{name: "preview", extra: []string{"--preview"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var out, errOut bytes.Buffer
			err := runOwnerBook(context.Background(), append(append([]string{}, base...), tc.extra...), strings.NewReader(tc.input), &out, &errOut)
			if (err != nil) != tc.wantError || calls != 0 {
				t.Fatal(err, calls)
			}
			if tc.name == "preview" && !strings.Contains(out.String(), pin) {
				t.Fatal(out.String())
			}
		})
	}
}

func TestOwnerBookPauseRetireTerminalAndConcurrentReadback(t *testing.T) {
	t.Setenv("MANTIK_API_TOKEN", "test-owner")
	at := time.Now().UTC()
	posts, gets := 0, 0
	for _, action := range []string{"pause_strategy", "retire_strategy"} {
		t.Run(action, func(t *testing.T) {
			a := account.OwnerBookAction{ExpectedVersion: 4, Action: action, StrategyID: "strategy-1"}
			a.Confirmation = a.ConfirmationPin("book", action)
			v := account.OwnerBookView{Config: account.DefaultCappedConfig("book", "account", account.Cash, at), Version: 5, AsOf: at, Halted: true, EquityUnavailable: "current marks unavailable", Sleeves: []account.OwnerSleeveView{{Sleeve: allocation.Sleeve{ID: "trend", StrategyID: a.StrategyID, Stage: "incubation"}, Paused: action == "pause_strategy", Retired: action == "retire_strategy", LastAllocation: &allocation.SleeveAllocation{Budget: "20", Status: "wait", Reason: "pending_orders"}}}}
			server := httptest.NewServer(loopbacktest.GoClientOnly(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("Authorization") != "Bearer test-owner" {
					t.Error("owner token missing")
				}
				if r.Method == "POST" {
					posts++
					var got account.OwnerBookAction
					if err := json.NewDecoder(r.Body).Decode(&got); err != nil || got != a {
						t.Error(got, err)
					}
					_ = json.NewEncoder(w).Encode(service.OwnerBookReceipt{ActionID: action, AppliedVersion: 5, Current: v})
				} else {
					gets++
					current := v
					current.Version = 6
					_ = json.NewEncoder(w).Encode(current)
				}
			})))
			defer server.Close()
			var out, errOut bytes.Buffer
			err := runOwnerBook(context.Background(), []string{"--api-url", server.URL, "--id", "book", "--action", action, "--strategy-id", a.StrategyID, "--action-id", action, "--expected-version", "4"}, strings.NewReader(a.Confirmation+"\n"), &out, &errOut)
			if err != nil || !strings.Contains(out.String(), "version=6") || !strings.Contains(out.String(), "halted=true") || !strings.Contains(out.String(), "pending_orders") || !strings.Contains(out.String(), "Equity=UNAVAILABLE") {
				t.Fatal(out.String(), err)
			}
		})
	}
	if posts != 2 || gets != 2 {
		t.Fatal(posts, gets)
	}
}

func TestOwnerBookAuthOriginAndRedirectBoundary(t *testing.T) {
	var out, errOut bytes.Buffer
	t.Setenv("MANTIK_API_TOKEN", "")
	t.Setenv("MANTIK_MODEL_TOKEN", "model-token-is-not-owner")
	if err := Run(context.Background(), []string{"book", "--id", "book"}, &out, &errOut); err == nil {
		t.Fatal("model credential accepted")
	}
	t.Setenv("MANTIK_API_TOKEN", "secret-canary")
	for _, origin := range []string{"http://example.com", "http://user:secret@example.com", "https://example.com/path", "https://example.com?token=x", "https://example.com#secret"} {
		if err := Run(context.Background(), []string{"book", "--id", "book", "--api-url", origin}, &out, &errOut); err == nil {
			t.Fatal("unsafe origin accepted")
		}
	}
	forwarded := 0
	target := httptest.NewServer(loopbacktest.GoClientOnly(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { forwarded++; http.Error(w, "secret-canary", 500) })))
	defer target.Close()
	server := httptest.NewServer(loopbacktest.GoClientOnly(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusTemporaryRedirect)
	})))
	defer server.Close()
	err := Run(context.Background(), []string{"book", "--id", "book", "--api-url", server.URL}, &out, &errOut)
	if err == nil || forwarded != 0 || strings.Contains(fmt.Sprint(err)+out.String()+errOut.String(), "secret-canary") {
		t.Fatal("redirect forwarded credential or logged secret", forwarded)
	}
	// Missing owner config is refused before any DB connection attempt.
	for _, action := range []string{"owner_clear_halt", "pause_strategy", "retire_strategy", "resume", "kill"} {
		if err := Run(context.Background(), []string{"capped-risk", "--action", action, "--database-env", "MANTIK_NEVER_CONNECT"}, &out, &errOut); err == nil || !strings.Contains(err.Error(), "refuses direct-database") {
			t.Fatal(action, err)
		}
	}
}

func TestOwnerBookFailedReadbackDoesNotClaimSuccess(t *testing.T) {
	t.Setenv("MANTIK_API_TOKEN", "test-owner")
	a := account.OwnerBookAction{ExpectedVersion: 0, Action: "owner_clear_halt"}
	a.Confirmation = a.ConfirmationPin("book", "clear")
	server := httptest.NewServer(loopbacktest.GoClientOnly(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "POST" {
			_ = json.NewEncoder(w).Encode(service.OwnerBookReceipt{ActionID: "clear", AppliedVersion: 1, Current: account.OwnerBookView{Config: account.CappedConfig{ID: "book"}, Version: 1}})
		} else {
			http.Error(w, "token-that-must-not-be-echoed", 503)
		}
	})))
	defer server.Close()
	var out, errOut bytes.Buffer
	err := runOwnerBook(context.Background(), []string{"--api-url", server.URL, "--id", "book", "--action", a.Action, "--action-id", "clear", "--expected-version", "0", "--confirm", a.Confirmation}, strings.NewReader(""), &out, &errOut)
	if err == nil || !strings.Contains(err.Error(), "action recorded at version 1; current readback unavailable") || strings.Contains(out.String(), "authoritative current readback follows") || errors.Is(err, context.Canceled) {
		t.Fatal(out.String(), err)
	}
}
