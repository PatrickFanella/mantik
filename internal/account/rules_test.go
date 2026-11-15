package account

import (
	"math/big"
	"testing"
	"time"
)

func rat(s string) *big.Rat {
	r, ok := new(big.Rat).SetString(s)
	if !ok {
		panic(s)
	}
	return r
}
func date(s string) time.Time {
	d, err := time.Parse(time.DateOnly, s)
	if err != nil {
		panic(err)
	}
	return d
}
func position(q, p string) Position { return Position{Asset: "x", Quantity: rat(q), Price: rat(p)} }
func trade(q, p, fee string) Trade {
	return Trade{Asset: "x", Quantity: rat(q), Price: rat(p), Fee: rat(fee)}
}

func TestRefusalsAndBoundaries(t *testing.T) {
	for _, tc := range []struct {
		name                         string
		kind                         Type
		cash, qty, price, order, fee string
		period                       Period
		unpaid                       string
		want                         Code
	}{
		{"cash short", Cash, "3000", "0", "100", "-1", "0", Overnight, "", ShortNotAllowed},
		{"cash free riding", Cash, "100", "0", "100", "2", "0", Overnight, "", FreeRiding},
		{"cash fee included", Cash, "100", "0", "100", "1", "0.01", Overnight, "", FreeRiding},
		{"paid same day sale", Cash, "0", "10", "100", "-10", "0", Overnight, "", ""},
		{"unpaid sale", Cash, "0", "10", "100", "-1", "0", Overnight, "10", GoodFaith},
		{"sell paid portion", Cash, "0", "10", "100", "-6", "0", Overnight, "4", ""},
		{"touch unpaid portion", Cash, "0", "10", "100", "-7", "0", Overnight, "4", GoodFaith},
		{"two times equity", Margin, "3000", "0", "100", "60", "0", Overnight, "", ""},
		{"above two times", Margin, "3000", "0", "100", "60.01", "0", Overnight, "", InitialMargin},
		{"fee consumes initial margin", Margin, "3000", "0", "100", "60", "0.01", Overnight, "", InitialMargin},
		{"margin short", Margin, "3000", "0", "100", "-30", "0", Overnight, "", ""},
		{"below minimum short", Margin, "1999.99", "0", "100", "-1", "0", Overnight, "", BelowMinimum},
		{"below minimum debit", Margin, "1999.99", "0", "100", "20", "0", Overnight, "", BelowMinimum},
		{"below minimum cash buy", Margin, "1999.99", "0", "100", "19", "0", Overnight, "", ""},
		{"exact minimum", Margin, "2000", "0", "100", "-1", "0", Overnight, "", ""},
		{"fee drops below minimum", Margin, "2000", "0", "100", "-1", "0.01", Overnight, "", BelowMinimum},
		{"reduce margin debit", Margin, "-3000", "40", "100", "-1", "0", Overnight, "", ""},
		{"close short below minimum", Margin, "6000", "-50", "100", "50", "0", Overnight, "", ""},
		{"reduce short while insolvent", Margin, "3000", "-40", "100", "1", "0", Overnight, "", ""},
		{"no long to short flip", Margin, "-3000", "40", "100", "-41", "0", Overnight, "", BelowMinimum},
		{"no short to long flip", Margin, "6000", "-50", "100", "61", "0", Overnight, "", BelowMinimum},
		{"intraday current excess", Margin, "3000", "0", "100", "90", "0", Intraday, "", ""},
		{"intraday maintenance deficit", Margin, "3000", "0", "100", "101", "0", Intraday, "", IntradayMargin},
		{"intraday small short per share floor", Margin, "3000", "0", "5", "-700", "0", Intraday, "", IntradayMargin},
		{"low priced long maintenance", Margin, "3000", "0", "2", "2000", "0", Overnight, "", MaintenanceMargin},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b := New(tc.kind, rat("0"))
			b.AddCash(rat(tc.cash))
			p := position(tc.qty, tc.price)
			if tc.unpaid != "" {
				p.Unpaid = rat(tc.unpaid)
			}
			before := b.Cash()
			got := b.Execute([]Position{p}, trade(tc.order, tc.price, tc.fee), tc.period, date("2026-01-05"), date("2026-01-06"))
			if got != tc.want {
				t.Fatalf("got %q, want %q", got, tc.want)
			}
			if got != "" && (b.Cash().Cmp(before) != 0 || b.Settled().Cmp(before) != 0) {
				t.Fatal("refusal changed balances")
			}
			if p.Quantity.Cmp(rat(tc.qty)) != 0 || p.Price.Cmp(rat(tc.price)) != 0 {
				t.Fatal("mutated caller positions")
			}
		})
	}
}

func TestSaleProceedsSettleBeforeReuse(t *testing.T) {
	b := New(Cash, rat("1000"))
	d, next := date("2026-01-09"), date("2026-01-12")
	if c := b.Execute([]Position{position("0", "100")}, trade("10", "100", "0"), Overnight, d, next); c != "" {
		t.Fatal(c)
	}
	if c := b.Execute([]Position{position("10", "100")}, trade("-10", "100", "1"), Overnight, d, next); c != "" {
		t.Fatal(c)
	}
	if b.Cash().Cmp(rat("999")) != 0 || b.Settled().Sign() != 0 {
		t.Fatal(b.Cash(), b.Settled())
	}
	for _, day := range []string{"2026-01-09", "2026-01-10", "2026-01-11"} {
		b.Advance(date(day))
		if c := b.Execute([]Position{position("0", "100")}, trade("1", "100", "0"), Overnight, date(day), next); c != UnsettledFunds {
			t.Fatal(day, c)
		}
	}
	b.AddCash(rat("100"))
	if b.Settled().Cmp(rat("100")) != 0 {
		t.Fatal("deposit released sale proceeds")
	}
	b.Advance(next)
	b.Advance(next)
	if b.Settled().Cmp(rat("1099")) != 0 {
		t.Fatal(b.Settled())
	}
	if c := b.Execute([]Position{position("0", "100")}, trade("10", "100", "0"), Overnight, next, date("2026-01-13")); c != "" {
		t.Fatal(c)
	}
}

func TestMarkToMarketShortfallAndRestoredMargin(t *testing.T) {
	b := New(Margin, rat("3000"))
	if c := b.Execute([]Position{position("0", "100")}, trade("60", "100", "0"), Overnight, date("2026-01-05"), date("2026-01-06")); c != "" {
		t.Fatal(c)
	}
	s := b.Status([]Position{position("60", "60")})
	if s.EffectiveType != Cash || s.Equity.Cmp(rat("600")) != 0 || s.Maintenance.Cmp(rat("1080")) != 0 || s.Shortfall.Cmp(rat("480")) != 0 {
		t.Fatalf("%+v", s)
	}
	b.AddCash(rat("2500"))
	s = b.Status([]Position{position("60", "60")})
	if s.EffectiveType != Margin || s.Shortfall.Sign() != 0 || s.OvernightBuyingPower.Cmp(rat("6200")) != 0 {
		t.Fatalf("%+v", s)
	}
	// Detached balances cannot mutate the book.
	s.Equity.SetInt64(0)
	b.Cash().SetInt64(0)
	if b.Cash().Cmp(rat("-500")) != 0 {
		t.Fatal("aliased readback")
	}
}

func TestCashFallbackCannotSpendShortCollateral(t *testing.T) {
	b := New(Margin, rat("6000"))
	positions := []Position{position("-30", "140"), {Asset: "y", Quantity: rat("0"), Price: rat("100")}}
	order := Trade{Asset: "y", Quantity: rat("19"), Price: rat("100"), Fee: rat("0")}
	if code := b.Execute(positions, order, Overnight, date("2026-01-05"), date("2026-01-06")); code != BelowMinimum {
		t.Fatal(code)
	}
	order.Quantity = rat("18")
	if code := b.Execute(positions, order, Overnight, date("2026-01-05"), date("2026-01-06")); code != "" {
		t.Fatal(code)
	}
}

func TestMaintenanceSchedule(t *testing.T) {
	for _, tc := range []struct{ qty, price, want string }{
		{"100", "2", "200"}, {"100", "2.50", "125"}, {"100", "6", "300"}, {"100", "6.01", "180.3"},
		{"-100", "1", "250"}, {"-100", "4", "400"}, {"-100", "5", "500"}, {"-100", "100", "3000"},
	} {
		if got := maintenance(position(tc.qty, tc.price)); got.Cmp(rat(tc.want)) != 0 {
			t.Fatal(tc, got)
		}
	}
}

func TestInvalidInputsRefused(t *testing.T) {
	if New("unknown", rat("1000")) != nil || New(Cash, nil) != nil || New(Cash, rat("-1")) != nil {
		t.Fatal("invalid book")
	}
	for name, change := range map[string]func(*Trade){
		"nil quantity": func(t *Trade) { t.Quantity = nil }, "zero quantity": func(t *Trade) { t.Quantity = rat("0") },
		"zero price": func(t *Trade) { t.Price = rat("0") }, "nil fee": func(t *Trade) { t.Fee = nil }, "negative fee": func(t *Trade) { t.Fee = rat("-1") },
	} {
		t.Run(name, func(t *testing.T) {
			b := New(Cash, rat("1000"))
			order := trade("1", "100", "0")
			change(&order)
			if c := b.Execute([]Position{position("0", "100")}, order, Overnight, date("2026-01-05"), date("2026-01-06")); c != InvalidTrade {
				t.Fatal(c)
			}
		})
	}
	b := New(Cash, rat("1000"))
	if c := b.Execute([]Position{position("0", "100")}, trade("1", "100", "0"), Overnight, date("2026-01-05"), date("2026-01-05")); c != InvalidTrade {
		t.Fatal(c)
	}
}
