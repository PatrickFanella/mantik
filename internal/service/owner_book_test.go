package service

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/account"
	"git.subcult.tv/subculture-collective/mantik/internal/cappedbook"
	"git.subcult.tv/subculture-collective/mantik/internal/risk"
	"git.subcult.tv/subculture-collective/mantik/internal/storage"
)

func ownerBookCall(t *testing.T, h http.Handler, method, path, token string, body any) *httptest.ResponseRecorder {
	t.Helper()
	var raw []byte
	if b, ok := body.([]byte); ok {
		raw = b
	} else if body != nil {
		var err error
		raw, err = json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
	}
	r := httptest.NewRequest(method, path, bytes.NewReader(raw))
	if token != "" {
		r.Header.Set("Authorization", "Bearer "+token)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func TestOwnerBookHTTPAuthenticationConfirmationAndRetries(t *testing.T) {
	ctx := context.Background()
	s := storage.NewMemory()
	at := time.Date(2026, 11, 22, 15, 0, 0, 0, time.UTC)
	cfg := account.DefaultCappedConfig("book", "account", account.Cash, at.AddDate(0, 0, -6))
	cfg.FundingMode = account.OwnerReceipts
	b, err := s.CreateCappedBook(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	b, err = s.ApplyCappedBook(ctx, "book", b.Version, account.BookCommand{ID: "halt", Kind: "halt", At: at})
	if err != nil {
		t.Fatal(err)
	}
	h := HandlerWithClock(s, "owner-test-token", newFakeClock(at))
	path := "/v1/capped-books/book/owner-controls/clear"
	a := account.OwnerBookAction{Action: "owner_clear_halt", ExpectedVersion: b.Version}
	a.Confirmation = a.ConfirmationPin("book", "clear")
	for _, token := range []string{"", "model-token", "someone-else"} {
		if w := ownerBookCall(t, h, "POST", path, token, a); w.Code != 401 {
			t.Fatal(w.Code, w.Body.String())
		}
	}
	for _, body := range []any{map[string]any{"Control": risk.Control{Action: "owner_clear_halt"}, "Owner": "owner"}, account.OwnerBookAction{ExpectedVersion: b.Version, Action: "owner_clear_halt", Confirmation: "yes"}, []byte(`{"Action":"owner_clear_halt","Action":"owner_clear_halt"}`), []byte(strings.Repeat(" ", 17<<10))} {
		if w := ownerBookCall(t, h, "POST", path, "owner-test-token", body); w.Code != 400 {
			t.Fatal(w.Code, w.Body.String())
		}
	}
	if w := ownerBookCall(t, h, "POST", path+"?owner=true", "owner-test-token", a); w.Code != 400 {
		t.Fatal(w.Code)
	}
	for _, method := range []string{"GET", "POST"} {
		p := path
		if method == "GET" {
			p = "/v1/capped-books/book/owner-view"
		}
		if w := ownerBookCall(t, HandlerWithClock(s, "", newFakeClock(at)), method, p, "", a); w.Code != 503 {
			t.Fatal(w.Code, w.Body.String())
		}
	}
	current, _ := s.CappedBook(ctx, "book")
	if current.Version != b.Version {
		t.Fatal("refusal mutated book")
	}
	for i := 0; i < 2; i++ {
		w := ownerBookCall(t, h, "POST", path, "owner-test-token", a)
		if w.Code != 200 {
			t.Fatal(w.Code, w.Body.String())
		}
		var got OwnerBookReceipt
		if err = json.Unmarshal(w.Body.Bytes(), &got); err != nil || got.Current.Halted || got.AppliedVersion != 2 || got.Current.Version != 2 {
			t.Fatal(got, err)
		}
	}
	changed := a
	changed.Action = "deposit"
	changed.Amount = "500"
	changed.Source = "actual-owner-receipt"
	changed.EffectiveAt = at
	changed.Confirmation = changed.ConfirmationPin("book", "clear")
	if w := ownerBookCall(t, h, "POST", path, "owner-test-token", changed); w.Code != 409 {
		t.Fatal(w.Code, w.Body.String())
	}
	// Real deposit is one acknowledged receipt; no other missed schedule slots appear.
	due, _ := cfg.DueDeposits(at)
	deposit := account.OwnerBookAction{Action: "deposit", ExpectedVersion: 2, Amount: "500", Source: "receipt-1", EffectiveAt: due[0].EffectiveAt.Add(time.Hour)}
	deposit.Confirmation = deposit.ConfirmationPin("book", due[0].ID)
	dp := "/v1/capped-books/book/owner-controls/" + due[0].ID
	for i := 0; i < 2; i++ {
		if w := ownerBookCall(t, h, "POST", dp, "owner-test-token", deposit); w.Code != 200 {
			t.Fatal(w.Code, w.Body.String())
		}
	}
	w := ownerBookCall(t, h, "GET", "/v1/capped-books/book/owner-view", "owner-test-token", nil)
	var view account.OwnerBookView
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &view) != nil || len(view.Deposits) != 1 || view.Cash != "2500.000000000" || view.Halted {
		t.Fatal(w.Code, w.Body.String())
	}
}

// The internal store can halt independently of the owner/model surface.
type rehaltingOwnerStore struct{ *storage.Memory }

func (s rehaltingOwnerStore) ApplyCappedBook(ctx context.Context, id string, v int64, c account.BookCommand) (account.CappedState, error) {
	b, err := s.Memory.ApplyCappedBook(ctx, id, v, c)
	if err == nil && c.RiskControl != nil && c.RiskControl.Action == "owner_clear_halt" {
		_, err = s.Memory.ApplyCappedBook(ctx, id, b.Version, account.BookCommand{ID: "concurrent-protective-halt", Kind: "halt", At: c.At.Add(time.Microsecond)})
	}
	return b, err
}

func TestOwnerBookClearReadbackShowsConcurrentRehalt(t *testing.T) {
	ctx := context.Background()
	s := rehaltingOwnerStore{storage.NewMemory()}
	at := time.Date(2026, 11, 16, 15, 0, 0, 0, time.UTC)
	b, _ := s.CreateCappedBook(ctx, account.DefaultCappedConfig("book", "account", account.Cash, at))
	b, _ = s.Memory.ApplyCappedBook(ctx, "book", b.Version, account.BookCommand{ID: "initial-halt", Kind: "halt", At: at})
	a := account.OwnerBookAction{Action: "owner_clear_halt", ExpectedVersion: b.Version}
	a.Confirmation = a.ConfirmationPin("book", "clear")
	for i := 0; i < 2; i++ {
		got, err := applyOwnerBookAction(ctx, s, "book", "clear", a, at.Add(time.Second))
		if err != nil || !got.Current.Halted || got.Current.Version != 3 || got.AppliedVersion != 2 {
			t.Fatal(got, err)
		}
	}
	b, _ = s.CappedBook(ctx, "book")
	if b.Version != 3 {
		t.Fatal("retry cleared new halt", b.Version)
	}
}

func TestOwnerBookVersionFenceAndPermanentRetirement(t *testing.T) {
	ctx := context.Background()
	r, _, base := reviewFixture(t, true)
	s := r.Store
	b, _ := s.CappedBook(ctx, r.BookID)
	roster, _ := account.PortfolioRoster(b)
	sid := roster["trend"].StrategyID
	at := base.At.Add(time.Second)
	h := HandlerWithClock(s.(*storage.Memory), "owner-token", newFakeClock(at))
	actions := []string{"pause_strategy", "retire_strategy"}
	codes := make(chan int, 2)
	var wg sync.WaitGroup
	for _, action := range actions {
		wg.Go(func() {
			a := account.OwnerBookAction{Action: action, StrategyID: sid, ExpectedVersion: b.Version}
			a.Confirmation = a.ConfirmationPin(r.BookID, action)
			codes <- ownerBookCall(t, h, "POST", "/v1/capped-books/"+r.BookID+"/owner-controls/"+action, "owner-token", a).Code
		})
	}
	wg.Wait()
	close(codes)
	passed, conflict := 0, 0
	for code := range codes {
		switch code {
		case 200:
			passed++
		case 409:
			conflict++
		default:
			t.Fatal(code)
		}
	}
	if passed != 1 || conflict != 1 {
		t.Fatal(passed, conflict)
	}
	for _, action := range []string{"retire_strategy", "resume_strategy", "owner_clear_halt"} {
		b, _ = s.CappedBook(ctx, r.BookID)
		id := "after-" + action
		a := account.OwnerBookAction{Action: action, StrategyID: sid, ExpectedVersion: b.Version}
		if action == "owner_clear_halt" {
			a.StrategyID = ""
		}
		a.Confirmation = a.ConfirmationPin(r.BookID, id)
		got, err := applyOwnerBookAction(ctx, s, r.BookID, id, a, at)
		if err != nil || !got.Current.Sleeves[0].Retired {
			t.Fatal(got, err)
		}
	}
}

func TestOwnerBookMissingValuationKeepsControlsVisibleAndRefusesClear(t *testing.T) {
	ctx := context.Background()
	r, _, base := reviewFixture(t, true)
	b, _ := r.Store.CappedBook(ctx, r.BookID)
	at := base.At.Add(2 * time.Minute)
	view, err := account.ReadOwnerBook(b, at)
	if err != nil || view.Equity != "" || view.EquityUnavailable == "" || len(view.Sleeves) != 1 || view.Sleeves[0].LastReview == nil || view.Sleeves[0].LastAllocation == nil {
		t.Fatal(view, err)
	}
	a := account.OwnerBookAction{Action: "owner_clear_halt", ExpectedVersion: b.Version}
	a.Confirmation = a.ConfirmationPin(r.BookID, "stale-clear")
	if _, err = applyOwnerBookAction(ctx, r.Store, r.BookID, "stale-clear", a, at); err == nil {
		t.Fatal("stale marks cleared halt")
	}
	a = account.OwnerBookAction{Action: "pause_strategy", StrategyID: "unknown", ExpectedVersion: b.Version}
	a.Confirmation = a.ConfirmationPin(r.BookID, "unknown-pause")
	if _, err = applyOwnerBookAction(ctx, r.Store, r.BookID, "unknown-pause", a, at); err == nil {
		t.Fatal("unknown strategy control accepted")
	}
	after, _ := r.Store.CappedBook(ctx, r.BookID)
	if after.Version != b.Version {
		t.Fatal("refusal mutated book")
	}
}

// Only identity is needed: Fund must never contact a broker.
type ownerFundingIdentityOnlyBroker struct{ cappedbook.Broker }

func (ownerFundingIdentityOnlyBroker) PaperAccountID() string { return "account" }

func TestOwnerBookMissedFlowsAfterRealReceipt(t *testing.T) {
	ctx := context.Background()
	s := storage.NewMemory()
	opened := time.Date(2026, 11, 16, 15, 0, 0, 0, time.UTC)
	cfg := account.DefaultCappedConfig("book", "account", account.Cash, opened)
	cfg.FundingMode = account.OwnerReceipts
	if _, err := s.CreateCappedBook(ctx, cfg); err != nil {
		t.Fatal(err)
	}
	at := opened.AddDate(0, 0, 6)
	due, _ := cfg.DueDeposits(at)
	a := account.OwnerBookAction{ExpectedVersion: 0, Action: "deposit", Amount: "500", Source: "isolated-Nov20-receipt", EffectiveAt: due[0].EffectiveAt.Add(time.Hour)}
	a.Confirmation = a.ConfirmationPin("book", due[0].ID)
	if _, err := applyOwnerBookAction(ctx, s, "book", due[0].ID, a, at); err != nil {
		t.Fatal(err)
	}
	later := time.Date(2026, 12, 20, 15, 0, 0, 0, time.UTC)
	client := cappedbook.Client{Store: s, Broker: ownerFundingIdentityOnlyBroker{}, BookID: "book"}
	b, err := client.Fund(ctx, later)
	if err != nil {
		t.Fatal(err)
	}
	v, err := account.ReadOwnerBook(b, later)
	if err != nil || v.Cash != "2500.000000000" || len(v.Deposits) != 1 || b.Version != 1 {
		t.Fatal("Dec4/Dec18 funded without receipts", v, err)
	}
	if v.Deposits[0].ID != "deposit-2026-11-20" {
		t.Fatal(v.Deposits)
	}
}

func TestOwnerBookHTTPRealDepositRefusesSyntheticBooks(t *testing.T) {
	for _, mode := range []string{"", account.SyntheticSchedule} {
		t.Run("mode="+mode, func(t *testing.T) {
			ctx := context.Background()
			s := storage.NewMemory()
			opened := time.Date(2026, 11, 16, 15, 0, 0, 0, time.UTC)
			at := opened.AddDate(0, 0, 6)
			cfg := account.DefaultCappedConfig("book", "account", account.Cash, opened)
			cfg.FundingMode = mode
			b, err := s.CreateCappedBook(ctx, cfg)
			if err != nil {
				t.Fatal(err)
			}
			due, _ := cfg.DueDeposits(at)
			a := account.OwnerBookAction{ExpectedVersion: 0, Action: "deposit", Amount: "500", Source: "isolated-receipt", EffectiveAt: due[0].EffectiveAt.Add(time.Hour)}
			a.Confirmation = a.ConfirmationPin("book", due[0].ID)
			h := HandlerWithClock(s, "owner-token", newFakeClock(at))
			if w := ownerBookCall(t, h, "POST", "/v1/capped-books/book/owner-controls/"+due[0].ID, "owner-token", a); w.Code != 422 {
				t.Fatal(w.Code, w.Body.String())
			}
			current, _ := s.CappedBook(ctx, "book")
			if current.Version != b.Version {
				t.Fatal("rejected receipt changed legacy book")
			}
			// Legacy books still support authenticated controls and authoritative reads.
			clear := account.OwnerBookAction{Action: "owner_clear_halt", ExpectedVersion: 0}
			clear.Confirmation = clear.ConfirmationPin("book", "clear")
			if w := ownerBookCall(t, h, "POST", "/v1/capped-books/book/owner-controls/clear", "owner-token", clear); w.Code != 200 {
				t.Fatal(w.Code, w.Body.String())
			}
			if w := ownerBookCall(t, h, "GET", "/v1/capped-books/book/owner-view", "owner-token", nil); w.Code != 200 {
				t.Fatal(w.Code, w.Body.String())
			}
		})
	}
}
