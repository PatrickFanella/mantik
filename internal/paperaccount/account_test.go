package paperaccount

import (
	"encoding/json"
	"testing"
	"time"
)

func TestFiniteCapitalAndReceiptIntegrity(t *testing.T) {
	c := Config{ID: "pilot", Currency: "USD", OpeningCashCents: 10000, CapitalLimitCents: 5000}
	a, err := Create("receiver-abcdefghijklmnop", c, time.Unix(100, 0))
	if err != nil || a.Validate() != nil || a.AllocationAvailable || a.QualificationCredit {
		t.Fatal(a, err)
	}
	if a.OpeningFunding.AmountCents != c.OpeningCashCents || a.OpeningFunding.Currency != "USD" {
		t.Fatal(a)
	}
	for _, bad := range []Config{{ID: "../bad", Currency: "USD", OpeningCashCents: 1, CapitalLimitCents: 1}, {ID: "a", Currency: "EUR", OpeningCashCents: 1, CapitalLimitCents: 1}, {ID: "a", Currency: "USD", OpeningCashCents: 0, CapitalLimitCents: 1}, {ID: "a", Currency: "USD", OpeningCashCents: MaxOpeningCashCents + 1, CapitalLimitCents: 1}, {ID: "a", Currency: "USD", OpeningCashCents: 1, CapitalLimitCents: 2}, {ID: "a", Currency: "USD", OpeningCashCents: 1, CapitalLimitCents: -1}} {
		if _, err := Create(a.ReceiverID, bad, a.CreatedAt); err == nil {
			t.Fatal("invalid config accepted", bad)
		}
	}
	for _, mutate := range []func(*Account){func(a *Account) { a.OpeningFunding.AmountCents++ }, func(a *Account) { a.SHA256 = "bad" }, func(a *Account) { a.AllocationAvailable = true }, func(a *Account) { a.QualificationCredit = true }, func(a *Account) { a.ReceiverID = "receiver-other_other_other" }, func(a *Account) { a.Policy = "margin" }} {
		bad := a
		mutate(&bad)
		if bad.Validate() == nil {
			t.Fatal("corrupt receipt accepted")
		}
	}
	if _, err := Create("bad", c, a.CreatedAt); err == nil {
		t.Fatal("receiver")
	}
	if _, err := Create(a.ReceiverID, c, time.Time{}); err == nil {
		t.Fatal("clock")
	}
	catalog, err := MakeCatalog(a.ReceiverID, []Account{a})
	if err != nil || catalog.Validate() != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(catalog)
	var detached Catalog
	_ = json.Unmarshal(raw, &detached)
	detached.Accounts[0].Config.OpeningCashCents++
	if detached.Validate() == nil {
		t.Fatal("changed catalogue accepted")
	}
	if _, err := MakeCatalog(a.ReceiverID, []Account{a, a}); err == nil {
		t.Fatal("duplicates")
	}
	empty, _ := MakeCatalog(a.ReceiverID, nil)
	if empty.Validate() != nil || empty.Accounts == nil {
		t.Fatal(empty)
	}
}
