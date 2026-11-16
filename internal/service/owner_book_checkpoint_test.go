package service

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/account"
	"git.subcult.tv/subculture-collective/mantik/internal/storage"
)

func TestOwnerBookCheckpointConfirmationRetryAndDailyBrief(t *testing.T) {
	ctx := context.Background()
	s := storage.NewMemory()
	at := time.Date(2026, 11, 16, 12, 0, 0, 0, time.UTC)
	b, err := s.CreateCappedBook(ctx, account.DefaultCappedConfig("book", "account", account.Cash, at))
	if err != nil {
		t.Fatal(err)
	}
	for i := range 4100 {
		b, err = s.ApplyCappedBook(ctx, "book", b.Version, account.BookCommand{ID: fmt.Sprint("observe-", i), Kind: "risk_observe", At: at})
		if err != nil {
			t.Fatal(i, err)
		}
	}
	b, err = s.ApplyCappedBook(ctx, "book", b.Version, account.BookCommand{ID: "initial-halt", Kind: "halt", At: at})
	if err != nil {
		t.Fatal(err)
	}
	h := HandlerWithClock(s, "owner-token", newFakeClock(at))
	a := account.OwnerBookAction{Action: "owner_clear_halt", ExpectedVersion: b.Version}
	a.Confirmation = a.ConfirmationPin("book", "clear")
	path := "/v1/capped-books/book/owner-controls/clear"
	for _, token := range []string{"", "model-token"} {
		if w := ownerBookCall(t, h, "POST", path, token, a); w.Code != 401 {
			t.Fatal("unauthenticated checkpoint clear", w.Code)
		}
	}
	w := ownerBookCall(t, h, "POST", path, "owner-token", a)
	var receipt OwnerBookReceipt
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &receipt) != nil || receipt.AppliedVersion != 4102 || receipt.Current.Halted {
		t.Fatal("logical clear confirmation refused", w.Code, w.Body.String())
	}
	b, err = s.ApplyCappedBook(ctx, "book", receipt.Current.Version, account.BookCommand{ID: "new-halt", Kind: "halt", At: at})
	if err != nil {
		t.Fatal(err)
	}
	w = ownerBookCall(t, h, "POST", path, "owner-token", a)
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &receipt) != nil || receipt.AppliedVersion != 4102 || receipt.Current.Version != b.Version || !receipt.Current.Halted {
		t.Fatal("retry changed receipt version or lost new halt", w.Code, w.Body.String())
	}
	stale := a
	stale.Confirmation = stale.ConfirmationPin("book", "stale-clear")
	if w = ownerBookCall(t, h, "POST", "/v1/capped-books/book/owner-controls/stale-clear", "owner-token", stale); w.Code != 409 {
		t.Fatal("stale confirmation cleared new halt", w.Code, w.Body.String())
	}
	before, err := json.Marshal(b)
	if err != nil {
		t.Fatal(err)
	}
	w = ownerBookCall(t, h, "POST", "/v1/capped-books/book/daily-brief-source", "owner-token", struct{ AsOf time.Time }{at})
	var source BriefSource
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &source) != nil {
		t.Fatal("checkpoint brief source", w.Code, w.Body.String())
	}
	brief, err := BuildDailyBrief(source, nil)
	if err != nil || brief.JournalVersion != b.Version || !brief.Halted || brief.Cash.Value != "2000.000000000" || brief.Deposits.Value != "0.000000000" || brief.SPYTWR.Unavailable == "" {
		t.Fatal("checkpoint brief", brief, err)
	}
	hash, err := BriefHash(b)
	if err != nil || brief.JournalSHA256 != hash {
		t.Fatal("brief changed source hash", err)
	}
	fresh, err := s.CappedBook(ctx, "book")
	after, marshalErr := json.Marshal(fresh)
	if err != nil || marshalErr != nil || string(before) != string(after) {
		t.Fatal("brief appended or changed retained state", err, marshalErr)
	}
	// The builder still refuses a malformed logical prefix after dropping its
	// old version==hot-slots check.
	source.Book.Version++
	if _, err = BuildDailyBrief(source, nil); err == nil {
		t.Fatal("invalid logical brief prefix accepted")
	}
}
