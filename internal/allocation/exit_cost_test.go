package allocation

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"reflect"
	"sync"
	"testing"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/paperaccount"
	"git.subcult.tv/subculture-collective/mantik/internal/paperfill"
	"git.subcult.tv/subculture-collective/mantik/internal/workerplan"
)

func modeledFill(t *testing.T, side string, price, fee int64, at time.Time) paperfill.Fill {
	t.Helper()
	model := paperfill.Model{Method: paperfill.Method, LiquidityBPS: 10000, MaxQuoteAgeSeconds: 60, CommissionPerFillCents: fee}
	order := paperfill.Order{ID: "modeled-" + side, Symbol: "ALPHA", Currency: "USD", Side: side, Kind: "market", Quantity: 1, PlacedAt: at, ExpiresAt: at.Add(time.Hour), Mode: "fixture"}
	if side == "buy" {
		order.MaxDebitCents = price + fee
	}
	journal, err := paperfill.New(model, order)
	if err != nil {
		t.Fatal(err)
	}
	quote := paperfill.Quote{ID: "quote-" + side, SourceSHA256: workerplan.Hash(side), Symbol: "ALPHA", Currency: "USD", Quality: "usable", EvidenceClass: "synthetic_unqualified", OccurredAt: at.Add(time.Second), ReceivedAt: at.Add(time.Second), BidCents: price, AskCents: price, BidQuantity: 1, AskQuantity: 1}
	_, projection, receipt, err := paperfill.Apply(context.Background(), journal, paperfill.Command{ID: "fill-" + side, At: quote.ReceivedAt, Quote: &quote})
	if err != nil || receipt.Outcome.Fill == nil || projection.ExecutionAttested || projection.QualificationCredit {
		t.Fatal(receipt, err)
	}
	return *receipt.Outcome.Fill
}

func TestLedgerModeledExitFeeReconciliation(t *testing.T) {
	legacy, admissions, at := ledgerFixture(t)
	ledger, err := NewCostLedger(legacy.Policy)
	if err != nil {
		t.Fatal(err)
	}
	buy := modeledFill(t, "buy", 10000, 3, at)
	sell := modeledFill(t, "sell", 1, 20, at.Add(2*time.Second))
	if buy.DebitCents != 10003 || sell.NetProceedsCents != -19 {
		t.Fatal(buy, sell)
	}
	q := proposal("modeled", "baseline-v1", "ALPHA", buy.DebitCents, at)
	ledger, _ = ledgerAppend(t, ledger, compareCommand("reserve-modeled", []Proposal{q}, admissions), at)
	ledger, _ = ledgerAppend(t, ledger, LedgerCommand{ID: "entry", Effect: &Effect{ID: buy.CommandID, SHA256: buy.SHA256, DecisionID: q.ID, Kind: "fill", AmountCents: buy.DebitCents}}, at.Add(time.Second))
	ledger, projection := ledgerAppend(t, ledger, LedgerCommand{ID: "exit", Effect: &Effect{ID: sell.CommandID, SHA256: sell.SHA256, DecisionID: q.ID, Kind: "close", AmountCents: buy.DebitCents, ProceedsCents: sell.NetProceedsCents}}, at.Add(3*time.Second))
	if projection.Snapshot.AvailableCashCents != 89978 || projection.Snapshot.RealizedPnLCents != -10022 || len(projection.Snapshot.Positions) != 0 || len(projection.Snapshot.Commitments) != 1 {
		t.Fatal(projection)
	}
	raw, err := json.Marshal(ledger)
	if err != nil {
		t.Fatal(err)
	}
	var restored Ledger
	if json.Unmarshal(raw, &restored) != nil {
		t.Fatal("restore")
	}
	again, err := ReplayLedger(context.Background(), restored)
	if err != nil || workerplan.Hash(again) != workerplan.Hash(projection) {
		t.Fatal(again, err)
	}
}

func TestLegacyLedgerWireHashesStayStable(t *testing.T) {
	ledger, admissions, at := ledgerFixture(t)
	if ledger.Schema != LedgerSchema || ledger.SHA256 != "4e0a2330875b5c1ea7a96822ddfd19bee78df2156aa7de8f2690718daf059f79" {
		t.Fatal("legacy empty hash changed", ledger.SHA256)
	}
	q := proposal("legacy", "baseline-v1", "ALPHA", 10000, at)
	ledger, _ = ledgerAppend(t, ledger, compareCommand("legacy-reserve", []Proposal{q}, admissions), at)
	ledger, _ = ledgerAppend(t, ledger, effectCommand("legacy-fill", q.ID, "fill", 10000, 0), at.Add(time.Second))
	ledger, _ = ledgerAppend(t, ledger, effectCommand("legacy-close", q.ID, "close", 10000, 11000), at.Add(2*time.Second))
	if ledger.SHA256 != "434b5a7a3242374060bdcf173c1c6ea3c34db631b3996fb44f1098336ca866ce" || ledger.Entries[2].SHA256 != "94effaf658827e3907fcd5d52d83601f0a090a901d2e842f34b121528dd4fac0" {
		t.Fatal("legacy history changed", ledger.SHA256, ledger.Entries[2].SHA256)
	}
}

func costLedgerPosition(t *testing.T) (Ledger, Proposal, time.Time) {
	t.Helper()
	legacy, admissions, at := ledgerFixture(t)
	ledger, err := NewCostLedger(legacy.Policy)
	if err != nil {
		t.Fatal(err)
	}
	q := proposal("cost-position", "baseline-v1", "ALPHA", 50000, at)
	ledger, _ = ledgerAppend(t, ledger, compareCommand("cost-reserve", []Proposal{q}, admissions), at)
	ledger, _ = ledgerAppend(t, ledger, effectCommand("cost-entry", q.ID, "fill", 40000, 0), at.Add(time.Second))
	ledger, _ = ledgerAppend(t, ledger, effectCommand("cost-unknown", q.ID, "unknown", 0, 0), at.Add(2*time.Second))
	return ledger, q, at.Add(3 * time.Second)
}
func refuseClose(t *testing.T, ledger Ledger, c LedgerCommand, at time.Time) {
	t.Helper()
	before := workerplan.Hash(ledger)
	next, projection, receipt, err := AppendLedger(context.Background(), ledger, c, func() time.Time { return at })
	if !errors.Is(err, ErrLedgerConflict) || !reflect.DeepEqual(next, Ledger{}) || !reflect.DeepEqual(projection, LedgerProjection{}) || !reflect.DeepEqual(receipt, LedgerReceipt{}) || workerplan.Hash(ledger) != before {
		t.Fatal("refusal changed state", err, next, projection, receipt)
	}
}
func TestCostLedgerCloseFundingAndHistoricalRetry(t *testing.T) {
	ledger, q, at := costLedgerPosition(t)
	// The remaining 10,000 reservation is not available to pay exit fees.
	for _, n := range []int64{-50001, -60000, -paperaccount.MaxOpeningCashCents - 1, math.MinInt64} {
		refuseClose(t, ledger, effectCommand("unfunded", q.ID, "close", 20000, n), at)
	}
	command := effectCommand("funded-exit", q.ID, "close", 20000, -50000)
	ledger, projection := ledgerAppend(t, ledger, command, at)
	if projection.Snapshot.AvailableCashCents != 0 || projection.Snapshot.RealizedPnLCents != -70000 || len(projection.Snapshot.Orders) != 1 || projection.Snapshot.Orders[0].AmountCents != 10000 || projection.Snapshot.Positions[0].AmountCents != 20000 || !projection.Reservations[0].Unknown {
		t.Fatal(projection)
	}
	refuseClose(t, ledger, effectCommand("one-cent-unfunded", q.ID, "close", 1, -1), at)
	// Even a self-rehashed debit cannot borrow a cent from held cash.
	forged := ledgerDetach(ledger)
	last := &forged.Entries[len(forged.Entries)-1]
	last.Command.Effect.ProceedsCents--
	last.SHA256 = ""
	last.SHA256 = workerplan.Hash(*last)
	forged.SHA256 = ""
	forged.SHA256 = workerplan.Hash(forged)
	if _, err := ReplayLedger(context.Background(), forged); err == nil {
		t.Fatal("rehashed unfunded close accepted")
	}
	// A known position may close while the entry remainder remains unknown.
	ledger, projection = ledgerAppend(t, ledger, effectCommand("zero-exit", q.ID, "close", 20000, 0), at.Add(time.Second))
	ledger, projection = ledgerAppend(t, ledger, effectCommand("resolve-remainder", q.ID, "resolve", 0, 0), at.Add(2*time.Second))
	ledger, projection = ledgerAppend(t, ledger, effectCommand("release-remainder", q.ID, "release", 10000, 0), at.Add(3*time.Second))
	if projection.Snapshot.AvailableCashCents != 10000 || projection.Snapshot.RealizedPnLCents != -90000 || len(projection.Snapshot.Orders) != 0 || len(projection.Snapshot.Positions) != 0 || len(projection.Snapshot.Commitments) != 1 {
		t.Fatal(projection)
	}
	again, current, retry, err := AppendLedger(context.Background(), ledger, command, func() time.Time { panic("historical close read clock") })
	if err != nil || !retry.Duplicate || retry.Sequence != 4 || !reflect.DeepEqual(again, ledger) || !reflect.DeepEqual(current, projection) {
		t.Fatal(retry, err)
	}
	changed := ledgerDetach(command)
	changed.Effect.ProceedsCents++
	if _, _, _, err = AppendLedger(context.Background(), ledger, changed, func() time.Time { panic("conflict read clock") }); !errors.Is(err, ErrLedgerConflict) {
		t.Fatal(err)
	}
	duplicate := ledgerDetach(command)
	duplicate.ID = "same-effect-another-command"
	refuseClose(t, ledger, duplicate, at.Add(4*time.Second))
}

func TestLedgerVersionsCannotReinterpretRetainedEntries(t *testing.T) {
	legacy, admissions, at := ledgerFixture(t)
	q := proposal("legacy-position", "baseline-v1", "ALPHA", 10000, at)
	legacy, _ = ledgerAppend(t, legacy, compareCommand("reserve", []Proposal{q}, admissions), at)
	legacy, _ = ledgerAppend(t, legacy, effectCommand("entry", q.ID, "fill", 10000, 0), at.Add(time.Second))
	refuseClose(t, legacy, effectCommand("legacy-negative-exit", q.ID, "close", 10000, -1), at.Add(2*time.Second))
	upgraded := ledgerDetach(legacy)
	upgraded.Schema = CostLedgerSchema
	upgraded.SHA256 = ""
	upgraded.SHA256 = workerplan.Hash(upgraded)
	if _, err := ReplayLedger(context.Background(), upgraded); err == nil {
		t.Fatal("schema-only rewrite accepted legacy entries")
	}
	cost, _, _ := costLedgerPosition(t)
	downgraded := ledgerDetach(cost)
	downgraded.Schema = LedgerSchema
	downgraded.SHA256 = ""
	downgraded.SHA256 = workerplan.Hash(downgraded)
	if _, err := ReplayLedger(context.Background(), downgraded); err == nil {
		t.Fatal("v2 entries reinterpreted as v1")
	}
	invalid := legacy.Policy
	invalid.SHA256 = workerplan.Hash("counterfeit")
	if _, err := NewCostLedger(invalid); !errors.Is(err, ErrInvalid) {
		t.Fatal("unfunded policy accepted", err)
	}
	if cost.Schema != CostLedgerSchema || legacy.Schema != LedgerSchema {
		t.Fatal(cost.Schema, legacy.Schema)
	}
}

func TestCostLedgerConcurrentCloseDoesNotCommitOrMutate(t *testing.T) {
	ledger, q, at := costLedgerPosition(t)
	before := workerplan.Hash(ledger)
	command := effectCommand("concurrent-close", q.ID, "close", 10000, -19)
	expected, _, _, err := AppendLedger(context.Background(), ledger, command, func() time.Time { return at })
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			next, projection, _, err := AppendLedger(context.Background(), ledger, command, func() time.Time { return at })
			if err != nil || next.SHA256 != expected.SHA256 || projection.Snapshot.AvailableCashCents != 49981 || projection.ExecutionAvailable || projection.QualificationCredit {
				t.Error("concurrent calculation changed", err)
			}
		})
	}
	wg.Wait()
	if workerplan.Hash(ledger) != before {
		t.Fatal("pure close mutated input")
	}
}
