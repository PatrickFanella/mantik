package papereconomics

import (
	"context"
	"errors"
	"math/big"
	"reflect"
	"strings"
	"testing"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/paperfill"
	"git.subcult.tv/subculture-collective/mantik/internal/workerplan"
)

// Buy two shares on Tuesday 2026-10-20 at 11:00 New York time, then observe
// one later quote on Thursday. Wednesday 2026-10-21 starts at 04:00 UTC (EDT).
var (
	entryAt = time.Date(2026, 10, 20, 15, 0, 0, 0, time.UTC)
	laterAt = time.Date(2026, 10, 22, 15, 0, 0, 0, time.UTC)
)

func trQuote(id string, at time.Time, bid, ask int64) paperfill.BookCommand {
	return paperfill.BookCommand{ID: id, At: at, Quote: &paperfill.Quote{ID: id, SourceSHA256: strings.Repeat("a", 64), Symbol: "SPY", Currency: "USD", Quality: "usable", EvidenceClass: "synthetic_unqualified", OccurredAt: at, ReceivedAt: at, BidCents: bid, AskCents: ask, BidQuantity: 10, AskQuantity: 10}}
}

func heldStudy(t *testing.T, later paperfill.BookCommand, sell bool) Input {
	t.Helper()
	m := paperfill.Model{Method: paperfill.Method, LiquidityBPS: 10000, MaxQuoteAgeSeconds: 2, CommissionPerFillCents: 5}
	order := paperfill.Order{ID: "entry", Symbol: "SPY", Currency: "USD", Side: "buy", Kind: "limit", Quantity: 2, LimitCents: 100, MaxDebitCents: 205, PlacedAt: entryAt, ExpiresAt: entryAt.Add(time.Minute), Mode: "replay"}
	b, err := paperfill.NewOccurrenceBook(m, []paperfill.Order{order})
	if err != nil {
		t.Fatal(err)
	}
	for _, q := range []paperfill.BookCommand{trQuote("first", entryAt.Add(time.Second), 99, 100), later} {
		if b, _, _, err = paperfill.ApplyBook(context.Background(), b, q); err != nil {
			t.Fatal(err)
		}
	}
	in := Input{OpeningCashCents: 1000, EntryBook: b, CutAt: later.At.Add(time.Second)}
	if sell {
		order.Side, order.Kind, order.LimitCents, order.MaxDebitCents = "sell", "market", 0, 0
		order.PlacedAt, order.ExpiresAt = later.At.Add(-time.Nanosecond), later.At.Add(time.Minute)
		exit, err := paperfill.NewOccurrenceBook(m, []paperfill.Order{order})
		if err != nil {
			t.Fatal(err)
		}
		if exit, _, _, err = paperfill.ApplyBook(context.Background(), exit, later); err != nil {
			t.Fatal(err)
		}
		in.ExitBook = &exit
	}
	return in
}

func window(actions ...Action) Actions {
	if actions == nil {
		actions = []Action{}
	}
	return Actions{Symbol: "SPY", WindowStart: "2026-10-01", WindowEnd: "2026-10-31", Actions: actions}
}

func dividend(ex, pay, amount string) Action {
	return Action{Kind: "cash_dividend", ExDate: ex, PayableDate: pay, Amount: amount}
}

func split(ex, from, to string) Action { return Action{Kind: "split", ExDate: ex, From: from, To: to} }

func evaluateTR(t *testing.T, in Input, a Actions) TotalReturn {
	t.Helper()
	r, err := EvaluateTotalReturn(context.Background(), in, a)
	if err != nil {
		t.Fatal(err)
	}
	u := r
	u.SHA256 = ""
	if r.SHA256 != workerplan.Hash(u) || r.InputSHA256 != workerplan.Hash(in) || r.ActionsSHA256 != workerplan.Hash(a) || r.AdmissionAvailable || r.ExecutionAvailable || r.QualificationCredit || r.Authority != "owner_selected_reconciled_total_return_accounting_only" {
		t.Fatal(r)
	}
	again, err := EvaluateTotalReturn(context.Background(), in, a)
	if err != nil || !reflect.DeepEqual(r, again) {
		t.Fatal("non-reproducible total return", err)
	}
	return r
}

func want(t *testing.T, r TotalReturn, status, cash, receivable, held, equity string) {
	t.Helper()
	got := ""
	if r.EquityCents != nil {
		got = *r.EquityCents
	}
	if r.Status != status || r.EndingCashCents != cash || r.DividendReceivableCents != receivable || r.HeldQuantity != held || got != equity {
		t.Fatalf("got %s cash=%s receivable=%s held=%s equity=%s", r.Status, r.EndingCashCents, r.DividendReceivableCents, r.HeldQuantity, got)
	}
}

func TestTotalReturnWithoutActionsMatchesObservedEquity(t *testing.T) {
	for _, sell := range []bool{false, true} {
		in := heldStudy(t, trQuote("later", laterAt, 110, 111), sell)
		observed, err := Evaluate(context.Background(), in)
		if err != nil {
			t.Fatal(err)
		}
		r := evaluateTR(t, in, window())
		terminal := observed.Points[len(observed.Points)-1]
		if !r.Available() || r.EquityCents == nil || terminal.EquityCents == nil || *r.EquityCents != big.NewInt(*terminal.EquityCents).String() || len(r.Applied) != 0 {
			t.Fatal(sell, r, terminal)
		}
	}
}

func TestDividendEntitlementAccruesThenPays(t *testing.T) {
	in := heldStudy(t, trQuote("later", laterAt, 110, 111), false)
	// Paid after the cut: 2 shares x 50.0 cents stay receivable in equity.
	r := evaluateTR(t, in, window(dividend("2026-10-21", "2026-10-30", "0.5")))
	want(t, r, "observed_total_return", "795", "100", "2", "1115")
	if *r.NetCents != "115" || len(r.Applied) != 1 || r.Applied[0].EntitlementCents != "100" || r.Applied[0].PaidAt != nil || !r.Applied[0].At.Equal(time.Date(2026, 10, 21, 4, 0, 0, 0, time.UTC)) {
		t.Fatal(r)
	}
	// Paid before the cut, at 00:00 New York time on the payable date.
	r = evaluateTR(t, in, window(dividend("2026-10-21", "2026-10-22", "0.5")))
	want(t, r, "observed_total_return", "895", "0", "2", "1115")
	if r.Applied[0].PaidAt == nil || !r.Applied[0].PaidAt.Equal(time.Date(2026, 10, 22, 4, 0, 0, 0, time.UTC)) {
		t.Fatal(r.Applied)
	}
	// An unknown payable date leaves the entitlement receivable.
	r = evaluateTR(t, in, window(dividend("2026-10-21", "", "0.5")))
	want(t, r, "observed_total_return", "795", "100", "2", "1115")
	// No rounding: 2 x 1.903516 USD.
	r = evaluateTR(t, in, window(dividend("2026-10-21", "", "1.903516")))
	want(t, r, "observed_total_return", "795", "380.7032", "2", "1395.7032")
	// Selling after the ex-date keeps the entitlement.
	r = evaluateTR(t, heldStudy(t, trQuote("later", laterAt, 110, 111), true), window(dividend("2026-10-21", "2026-10-30", "0.5")))
	want(t, r, "observed_total_return", "1010", "100", "0", "1110")
	if r.RealizedNetCents != "10" || r.RemainingCostCents != "0" {
		t.Fatal(r)
	}
}

func TestActionsOutsideTheHoldingAreIgnored(t *testing.T) {
	in := heldStudy(t, trQuote("later", laterAt, 110, 111), false)
	// Ex-date 2026-10-20 starts before the first placement; 2026-10-23 is after the cut.
	r := evaluateTR(t, in, window(dividend("2026-10-20", "2026-10-21", "9"), split("2026-10-23", "1", "2")))
	want(t, r, "observed_total_return", "795", "0", "2", "1015")
	if len(r.Applied) != 0 {
		t.Fatal(r.Applied)
	}
}

func TestSplitsAdjustQuantityAndPreserveBasis(t *testing.T) {
	r := evaluateTR(t, heldStudy(t, trQuote("later", laterAt, 55, 56), false), window(split("2026-10-21", "1", "2")))
	want(t, r, "observed_total_return", "795", "0", "4", "1015")
	if r.RemainingCostCents != "205" || r.Applied[0].QuantityBefore != "2" || r.Applied[0].QuantityAfter != "4" {
		t.Fatal(r)
	}
	// A 3-for-1 reverse split leaves an exact repeating fraction.
	r = evaluateTR(t, heldStudy(t, trQuote("later", laterAt, 330, 331), false), window(split("2026-10-21", "3", "1")))
	want(t, r, "observed_total_return", "795", "0", "2/3", "1015")
	// Selling the original two shares after it exceeds the adjusted position.
	r = evaluateTR(t, heldStudy(t, trQuote("later", laterAt, 330, 331), true), window(split("2026-10-21", "3", "1")))
	if r.Status != "fill_exceeds_split_adjusted_position" || r.EquityCents != nil || r.NetCents != nil {
		t.Fatal(r)
	}
	// Selling two of four split shares closes half the preserved basis.
	r = evaluateTR(t, heldStudy(t, trQuote("later", laterAt, 55, 56), true), window(split("2026-10-21", "1", "2")))
	want(t, r, "observed_total_return", "900", "0", "2", "1010")
	if r.RemainingCostCents != "102.5" || r.RealizedNetCents != "2.5" {
		t.Fatal(r)
	}
}

func TestHoldingMustStayInsideTheCoverageWindow(t *testing.T) {
	in := heldStudy(t, trQuote("later", laterAt, 110, 111), false)
	for _, w := range [][2]string{{"2026-10-21", "2026-10-21"}, {"2026-10-22", "2026-10-31"}} {
		a := window()
		a.WindowStart, a.WindowEnd = w[0], w[1]
		if r := evaluateTR(t, in, a); r.Status != "holding_outside_corporate_coverage_window" || r.EquityCents != nil {
			t.Fatal(w, r)
		}
	}
	a := window()
	a.WindowStart, a.WindowEnd = "2026-10-21", "2026-10-22"
	if r := evaluateTR(t, in, a); !r.Available() {
		t.Fatal(r)
	}
	// A closed position needs no coverage after it closes.
	closed := heldStudy(t, trQuote("later", laterAt, 110, 111), true)
	closed.CutAt = laterAt.Add(5 * 24 * time.Hour)
	if r := evaluateTR(t, closed, a); !r.Available() || *r.EquityCents != "1010" {
		t.Fatal(r)
	}
}

func TestTerminalMarkRules(t *testing.T) {
	boundary := time.Date(2026, 10, 21, 4, 0, 0, 0, time.UTC)
	in := heldStudy(t, trQuote("later", boundary.Add(-500*time.Millisecond), 110, 111), false)
	in.CutAt = boundary.Add(500 * time.Millisecond)
	if r := evaluateTR(t, in, window()); !r.Available() || r.MarkQuoteID != "later" {
		t.Fatal(r)
	}
	if r := evaluateTR(t, in, window(dividend("2026-10-21", "", "0.5"))); r.Status != "terminal_mark_predates_corporate_action" || r.EquityCents != nil || r.DividendReceivableCents != "100" {
		t.Fatal(r)
	}
	stale := heldStudy(t, trQuote("later", laterAt, 110, 111), false)
	stale.CutAt = laterAt.Add(3 * time.Second)
	if r := evaluateTR(t, stale, window()); r.Status != "terminal_mark_unavailable" {
		t.Fatal(r)
	}
}

func TestTotalReturnRefusesMalformedActions(t *testing.T) {
	in := heldStudy(t, trQuote("later", laterAt, 110, 111), false)
	bad := []Actions{
		{Symbol: "SPY", WindowStart: "2026-10-01", WindowEnd: "2026-10-31"},
		{Symbol: "QQQ", WindowStart: "2026-10-01", WindowEnd: "2026-10-31", Actions: []Action{}},
		{Symbol: "SPY", WindowStart: "2026-10-31", WindowEnd: "2026-10-01", Actions: []Action{}},
		{Symbol: "SPY", WindowStart: "2026-10-1", WindowEnd: "2026-10-31", Actions: []Action{}},
		window(dividend("2026-11-02", "", "1")),
		window(dividend("2026-10-21", "2026-10-20", "1")),
		window(dividend("2026-10-21", "", "one")),
		window(dividend("2026-10-21", "", "1/2")),
		window(dividend("2026-10-21", "", "0")),
		window(dividend("2026-10-21", "", "-1")),
		window(split("2026-10-21", "1", "")),
		window(Action{Kind: "spinoff", ExDate: "2026-10-21"}),
		window(Action{Kind: "cash_dividend", ExDate: "2026-10-21", Amount: "1", From: "1"}),
	}
	for i, a := range bad {
		if _, err := EvaluateTotalReturn(context.Background(), in, a); !errors.Is(err, ErrInvalid) {
			t.Fatal(i, err)
		}
	}
	for i, a := range bad[2:] {
		if a.Validate() == nil {
			t.Fatal("validated", i)
		}
	}
	if window(dividend("2026-10-21", "", "19035.16e-4")).Validate() != nil {
		t.Fatal("exponent notation is exact")
	}
	many := window()
	for range MaxActions + 1 {
		many.Actions = append(many.Actions, dividend("2026-10-21", "", "1"))
	}
	if _, err := EvaluateTotalReturn(context.Background(), in, many); !errors.Is(err, ErrInvalid) {
		t.Fatal(err)
	}
}

func TestSameExDateActions(t *testing.T) {
	in := heldStudy(t, trQuote("later", laterAt, 110, 111), false)
	// A regular and a special dividend on one ex-date both apply.
	r := evaluateTR(t, in, window(dividend("2026-10-21", "", "0.5"), dividend("2026-10-21", "", "0.25")))
	want(t, r, "observed_total_return", "795", "150", "2", "1165")
	for _, a := range []Actions{
		window(dividend("2026-10-21", "", "1"), split("2026-10-21", "1", "2")),
		window(split("2026-10-21", "1", "2"), split("2026-10-21", "1", "3")),
	} {
		r := evaluateTR(t, in, a)
		if r.Status != "same_ex_date_split_and_other_action_unsupported" || r.EquityCents != nil || len(r.Applied) != 0 {
			t.Fatal(r)
		}
	}
}

func TestExactText(t *testing.T) {
	for in, out := range map[string]string{"10": "10", "1903.516": "1903.516", "1/3": "1/3", "-1/4": "-0.25", "0": "0", "7/40": "0.175", "1/6": "1/6"} {
		r, _ := new(big.Rat).SetString(in)
		if got := exactText(r); got != out {
			t.Fatal(in, got)
		}
	}
}
