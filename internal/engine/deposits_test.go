package engine

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"git.subcult.tv/subculture-collective/mantik/internal/marketdata"
	"testing"
	"time"
)

func TestDepositsFallOnAlternateFridaysAtSixChicagoTime(t *testing.T) {
	opened := time.Date(2026, 10, 20, 15, 0, 0, 0, time.UTC) // a Tuesday
	got, err := DefaultDeposits.Due(opened, time.Date(2026, 12, 31, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	// Daylight saving ends on November 1: 06:00 Chicago moves from 11:00 to
	// 12:00 UTC.
	want := []struct {
		id string
		at time.Time
	}{
		{"deposit-2026-10-23", time.Date(2026, 10, 23, 11, 0, 0, 0, time.UTC)},
		{"deposit-2026-11-06", time.Date(2026, 11, 6, 12, 0, 0, 0, time.UTC)},
		{"deposit-2026-11-20", time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)},
		{"deposit-2026-12-04", time.Date(2026, 12, 4, 12, 0, 0, 0, time.UTC)},
		{"deposit-2026-12-18", time.Date(2026, 12, 18, 12, 0, 0, 0, time.UTC)},
	}
	if len(got) != len(want) {
		t.Fatal(got)
	}
	for i, w := range want {
		if got[i].ID != w.id || !got[i].EffectiveAt.Equal(w.at) || got[i].Amount != "500" {
			t.Fatal(i, got[i])
		}
	}
	// Daylight saving starts on March 14, 2027: back to 11:00 UTC.
	spring, _ := DefaultDeposits.Due(time.Date(2027, 3, 1, 0, 0, 0, 0, time.UTC), time.Date(2027, 3, 31, 0, 0, 0, 0, time.UTC))
	if len(spring) != 2 || spring[0].EffectiveAt.Hour() != 12 || spring[1].ID != "deposit-2027-03-19" || spring[1].EffectiveAt.Hour() != 11 {
		t.Fatal(spring)
	}
	// A book opening at the deposit time takes that deposit; one opening a
	// minute later starts with the following Friday.
	at := time.Date(2026, 10, 23, 11, 0, 0, 0, time.UTC)
	if d, _ := DefaultDeposits.Due(at, at); len(d) != 1 {
		t.Fatal("deposit at opening missed")
	}
	if d, _ := DefaultDeposits.Due(at.Add(time.Minute), at.AddDate(0, 0, 7)); len(d) != 1 || d[0].ID != "deposit-2026-10-30" {
		t.Fatal(d)
	}
	for _, bad := range []DepositSchedule{{Amount: "0", EveryWeeks: 2}, {Amount: "500", EveryWeeks: 0}, {Amount: "1e3", EveryWeeks: 2}} {
		if _, err := bad.Due(opened, opened); !errors.Is(err, ErrInvalidConfig) {
			t.Error(bad, err)
		}
	}
}

func depositConfig(src source, deposits *DepositSchedule) Config {
	c := config(src, allIn{}, CostModel{Version: "free", FeeBps: "0", SlippageBps: "0"})
	c.Start = time.Date(2026, 1, 6, 0, 0, 0, 0, time.UTC) // first deposit Friday January 9
	c.Deposits = deposits
	return c
}

func TestDepositArrivesAsCashBeforeTheOpen(t *testing.T) {
	r, err := Run(context.Background(), depositConfig(newSource(t, prices), &DefaultDeposits))
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Deposits) != 1 || r.Deposits[0].ID != "deposit-2026-01-09" || r.Deposits[0].AppliedOn != "2026-01-09" || !r.Deposits[0].EffectiveAt.Equal(time.Date(2026, 1, 9, 12, 0, 0, 0, time.UTC)) {
		t.Fatalf("%+v", r.Deposits)
	}
	friday := r.Days[len(r.Days)-1]
	if friday.Date != "2026-01-09" || friday.Deposited != "500" {
		t.Fatalf("%+v", friday)
	}
	// The all-in strategy spends the deposit at Friday's open of 106.
	last := r.Fills[len(r.Fills)-1]
	if last.Date != "2026-01-09" || friday.Cash != "0" {
		t.Fatalf("deposit not usable at the open: %+v %+v", last, friday)
	}
}

func TestLateSessionAppliesDepositOnceWithItsScheduledTime(t *testing.T) {
	// No bar on Friday January 9: the deposit arrives before Monday's open.
	src := sourceOn(t, []int{5, 6, 7, 8, 12}, prices)
	var first []byte
	for i := 0; i < 2; i++ {
		c := depositConfig(src, &DefaultDeposits)
		c.End = closeAt(12).Add(time.Hour)
		r, err := Run(context.Background(), c)
		if err != nil {
			t.Fatal(err)
		}
		if len(r.Deposits) != 1 || r.Deposits[0].AppliedOn != "2026-01-12" || !r.Deposits[0].EffectiveAt.Equal(time.Date(2026, 1, 9, 12, 0, 0, 0, time.UTC)) {
			t.Fatalf("%+v", r.Deposits)
		}
		deposited := 0
		for _, d := range r.Days {
			if d.Deposited != "0" {
				deposited++
			}
		}
		raw, _ := json.Marshal(r)
		if deposited != 1 || first != nil && !bytes.Equal(first, raw) {
			t.Fatal("deposit applied more than once or replay differed")
		}
		first = raw
	}
}

func TestDepositOnAFlatDayLeavesTimeWeightedReturnUnchanged(t *testing.T) {
	flat := [][2]string{{"100", "100"}, {"100", "100"}, {"100", "100"}, {"100", "100"}, {"100", "100"}}
	with, err := Run(context.Background(), depositConfig(newSource(t, flat), &DefaultDeposits))
	if err != nil {
		t.Fatal(err)
	}
	without, err := Run(context.Background(), depositConfig(newSource(t, flat), nil))
	if err != nil {
		t.Fatal(err)
	}
	for i := range with.Days {
		w, o := with.Days[i], without.Days[i]
		if w.Return != "0" || w.TWR != "0" || w.Return != o.Return || w.TWR != o.TWR {
			t.Fatalf("day %d: %+v vs %+v", i, w, o)
		}
	}
	if last := with.Days[len(with.Days)-1]; last.Equity != "1500" {
		t.Fatal("deposit missing from equity", last.Equity)
	}
}

func TestTimeWeightedReturnChainsDailyReturns(t *testing.T) {
	r, err := Run(context.Background(), depositConfig(newSource(t, prices), &DefaultDeposits))
	if err != nil {
		t.Fatal(err)
	}
	// The strategy is fully invested from the January 7 open. The deposit on
	// the 9th is spent at that open, so the chained return follows the price
	// path and does not count the $500.
	no, err := Run(context.Background(), depositConfig(newSource(t, prices), nil))
	if err != nil {
		t.Fatal(err)
	}
	if a, b := r.Days[len(r.Days)-1].TWR, no.Days[len(no.Days)-1].TWR; a != b {
		t.Fatal("deposit changed the time-weighted return", a, b)
	}
}

func TestDepositDoesNotDiluteTheOvernightReturn(t *testing.T) {
	// Fully invested at 100 from January 7; Friday opens and closes at 110,
	// the morning a $500 deposit arrives.
	gap := [][2]string{{"100", "100"}, {"100", "100"}, {"100", "100"}, {"100", "100"}, {"110", "110"}}
	with, err := Run(context.Background(), depositConfig(newSource(t, gap), &DefaultDeposits))
	if err != nil {
		t.Fatal(err)
	}
	without, err := Run(context.Background(), depositConfig(newSource(t, gap), nil))
	if err != nil {
		t.Fatal(err)
	}
	w, o := with.Days[len(with.Days)-1], without.Days[len(without.Days)-1]
	if w.Deposited != "500" || w.Return != "0.1" || o.Return != "0.1" || w.TWR != o.TWR {
		t.Fatalf("with %+v without %+v", w, o)
	}
}

func TestDepositBetweenDividendExAndPayDoesNotCreateReturn(t *testing.T) {
	known := closeAt(1)
	src := sourceOn(t, []int{5, 6, 7, 8, 9, 12, 13}, [][2]string{{"100", "100"}, {"100", "100"}, {"100", "100"}, {"99", "99"}, {"99", "99"}, {"99", "99"}, {"99", "99"}}, marketdata.CorporateAction{SourceID: "dividend", Kind: "cash_dividend", ExDate: "2026-01-08", PayDate: "2026-01-13", Amount: "1", KnownAt: known})
	cfg := config(src, allIn{}, CostModel{Version: "zero", FeeBps: "0", SlippageBps: "0"})
	cfg.Start = time.Date(2026, 1, 5, 0, 0, 0, 0, time.UTC)
	cfg.End = closeAt(13).Add(time.Hour)
	cfg.Unlevered = true
	cfg.Deposits = &DepositSchedule{Amount: "500", EveryWeeks: 2}
	r, e := Run(context.Background(), cfg)
	if e != nil {
		t.Fatal(e)
	}
	for _, d := range r.Days {
		if d.Return != "0" || d.TWR != "0" {
			t.Fatal("dividend receivable/deposit generated return", d)
		}
	}
	if r.Days[3].Receivable != "10" || r.Days[3].Cash != "0" || r.Days[3].Equity != "1000" {
		t.Fatal("earned dividend not valued independently of spendable cash", r.Days[3])
	}
	if r.Days[4].Deposited != "500" || r.Days[6].Receivable != "0" {
		t.Fatal(r.Days)
	}
}
