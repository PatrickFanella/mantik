package storage

import (
	"context"
	"reflect"
	"strings"
	"testing"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/account"
	"git.subcult.tv/subculture-collective/mantik/internal/allocation"
	"git.subcult.tv/subculture-collective/mantik/internal/paperfill"
	"git.subcult.tv/subculture-collective/mantik/internal/workerplan"
)

func TestPaperEconomicsRetainsUnknownFeesAndCountsOnlyEntryShares(t *testing.T) {
	for _, tc := range []struct {
		name    string
		missing int
	}{
		{"complete", -1},
		{"historical entry", 0},
		{"historical close", 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			effects := []PaperExecutionEffect{
				{Kind: "fill", AmountCents: 2003, Execution: &paperfill.Fill{Quantity: 2, FeeCents: 3}},
				{Kind: "close", AmountCents: 1001, ProceedsCents: 1096, Execution: &paperfill.Fill{Quantity: 1, FeeCents: 4}},
			}
			if tc.missing >= 0 {
				effects[tc.missing].Execution = nil
			}
			e := paperEconomicsFromEffects(3000, effects)
			if e.Status != "position_open" || e.RealizedPnLCents == nil || *e.RealizedPnLCents != 95 {
				t.Fatal("explicit net cost/proceeds became unavailable", e)
			}
			if tc.missing >= 0 {
				if e.FeesCents != nil || e.ExecutedQuantity != nil {
					t.Fatal("partial historical detail became a complete fee/share total", e)
				}
			} else if e.FeesCents == nil || *e.FeesCents != 7 || e.ExecutedQuantity == nil || *e.ExecutedQuantity != 2 {
				t.Fatal("close quantity doubled entry quantity or fees were lost", e)
			}
		})
	}
}

func TestOwnedExecutionKeepsLegacyFinancialContract(t *testing.T) {
	f := newEligibilityFixture(t)
	f.exec = &ownedExecution{feed: f.m.decisionFeedLocked(), quotes: fixtureExecutionQuotes(1)}
	journal, _, err := f.append(t, ownedAllocationJournal{Records: []retainedOwnedAllocation{}}, f.command, f.at, f.complete)
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := f.replay(t, journal, f.complete)
	if err != nil {
		t.Fatal(err)
	}
	// Recreate a genuine v16 initial receipt from the old reducer contract.
	old, err := allocation.NewCostLedger(catalog.Ledgers[0].Policy)
	if err != nil {
		t.Fatal(err)
	}
	r := &journal.Records[0].Record
	old, _, _, err = allocation.AppendLedger(context.Background(), old, r.Entry.Command, func() time.Time { return r.At })
	if err != nil {
		t.Fatal(err)
	}
	r.Schema, r.Entry, r.LedgerSHA256 = legacyOwnedExecutionSchema, old.Entries[0], old.SHA256
	for i := range r.AdmissionChecks {
		check := &r.AdmissionChecks[i]
		check.Binding.LedgerSHA256 = old.SHA256
		check.SHA256 = ""
		check.SHA256 = workerplan.Hash(*check)
	}
	r.SHA256 = ""
	r.SHA256 = workerplan.Hash(*r)
	before := workerplan.Hash(journal)
	if _, err := f.replay(t, journal, f.complete); err != nil {
		t.Fatal("v16 receipt no longer replays", err)
	}
	command := OwnedExecutionCommand{ID: "legacy-step", ReceiverID: f.m.receiverID}
	stepped, receipt, err := f.step(t, journal, command, f.at.Add(2*time.Second), f.complete)
	if err != nil || receipt.Record == nil {
		t.Fatal(receipt, err)
	}
	for _, x := range receipt.Record.Executions {
		for _, e := range x.Entries {
			if e.Command.Effect.Execution != nil {
				t.Fatal("old ledger acquired inferred fill detail")
			}
		}
	}
	again, retry, err := f.step(t, stepped, command, f.at.Add(time.Hour), f.complete)
	if err != nil || !retry.Duplicate || !reflect.DeepEqual(again, stepped) || workerplan.Hash(journal) != before {
		t.Fatal("legacy retry or retained prefix changed", err)
	}
}

func bookFillAttributionStorageContract(t *testing.T, s CappedBookStore) {
	t.Helper()
	ctx := t.Context()
	at := time.Date(2026, 11, 16, 15, 0, 0, 0, time.UTC)
	state, err := s.CreateCappedBook(ctx, account.DefaultCappedConfig("fill-attribution", "isolated", account.Cash, at))
	if err != nil {
		t.Fatal(err)
	}
	order := account.BookOrder{ID: "fractional-buy", Symbol: "SPY", Quantity: "0.125", Limit: "100", Fee: "0.02", Side: "buy", At: at}
	fill := account.BookFill{ID: strings.Repeat("a", 119) + ":activity", OrderID: order.ID, VenueOrderID: "venue", Symbol: order.Symbol, Quantity: order.Quantity, Price: "100", Fee: "0.01", Side: order.Side, SourceSHA256: strings.Repeat("a", 64), At: at.Add(time.Second)}
	commands := []account.BookCommand{
		{ID: order.ID, Kind: "reserve", At: at, Order: &order, Marks: map[string]string{"SPY": "100"}},
		{ID: "bind", Kind: "bind", At: at, OrderID: order.ID, VenueOrderID: fill.VenueOrderID},
		{ID: fill.ID, Kind: "fill", At: at.Add(2 * time.Second), Fill: &fill, Marks: map[string]string{"SPY": "100"}},
	}
	for _, c := range commands {
		state, err = s.ApplyCappedBook(ctx, state.Config.ID, state.Version, c)
		if err != nil {
			t.Fatal(c.Kind, err)
		}
	}
	read := func(store CappedBookStore) account.OwnerBookView {
		t.Helper()
		got, err := store.CappedBook(ctx, state.Config.ID)
		if err != nil || !reflect.DeepEqual(got, state) {
			t.Fatal("retained fill state differs", err)
		}
		v, err := account.ReadOwnerBook(got, at.Add(3*time.Second))
		if err != nil || v.Execution == nil || v.Execution.Fees != "0.01" || v.Execution.GrossBuys != "12.5" || v.Execution.ReconciledCash != "1987.49" || v.Execution.Validate(v.Cash) != nil || len(v.Execution.Fills) != 1 || v.Execution.Fills[0].Fill != fill {
			t.Fatal("durable fractional accounting differs", v, err)
		}
		return v
	}
	before := read(s)
	if p, ok := s.(*Postgres); ok {
		reopened, err := OpenPostgres(ctx, p.pool.Config().ConnString())
		if err != nil {
			t.Fatal(err)
		}
		defer reopened.Close()
		after := read(reopened)
		if !reflect.DeepEqual(before.Execution, after.Execution) {
			t.Fatal("restart changed original financial attribution")
		}
	}
}

func TestMemoryBookFillAttribution(t *testing.T) {
	bookFillAttributionStorageContract(t, NewMemory())
}

func TestPostgresContractsBookFillAttribution(t *testing.T) {
	bookFillAttributionStorageContract(t, timerPostgres(t, "fill_attribution"))
}
