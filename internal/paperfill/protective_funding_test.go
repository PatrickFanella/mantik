package paperfill

import (
	"testing"
)

func TestExitShortfallCeilingBoundsPartitionsAndPriceGaps(t *testing.T) {
	for _, fee := range []int64{0, 1, 10, 999, 1000} {
		for _, commission := range []int64{0, 1, 3, 1000000} {
			m := Model{Method: Method, FeeBPS: fee, CommissionPerFillCents: commission, LiquidityBPS: 10000, MaxQuoteAgeSeconds: 60}
			for quantity := int64(1); quantity <= 12; quantity++ {
				ceiling, err := ExitShortfallCeiling(m, quantity)
				if err != nil {
					t.Fatal(err)
				}
				// Every ordered partition of this small quantity, at price floor,
				// ordinary prices and the method's largest price. Costs are from
				// the actual fill method, not a duplicate implementation of fees.
				for _, price := range []int64{1, 2, 9, 10, 99, 100, 10001, MaxPriceCents} {
					var visit func(int64, int64)
					visit = func(left, proceeds int64) {
						if left == 0 {
							if proceeds < -ceiling {
								t.Fatalf("unfunded exit: model=%+v quantity=%d price=%d proceeds=%d ceiling=%d", m, quantity, price, proceeds, ceiling)
							}
							return
						}
						for n := int64(1); n <= left; n++ {
							gross, charge, ok := cost(m, price, n)
							if !ok {
								t.Fatal("supported fill rejected")
							}
							visit(left-n, proceeds+gross-charge)
						}
					}
					visit(quantity, 0)
				}
				gross, charge, _ := cost(m, 1, 1)
				if max(int64(0), quantity*(charge-gross)) != ceiling {
					t.Fatal("ceiling not tight at one-cent one-share fills")
				}
			}
		}
	}
}

func TestExitShortfallCeilingRejectsUnsupportedInputs(t *testing.T) {
	m := Model{Method: Method, CommissionPerFillCents: 1000000, LiquidityBPS: 10000, MaxQuoteAgeSeconds: 60}
	if v, err := ExitShortfallCeiling(m, MaxQuantity); err != nil || v != 999999000000 {
		t.Fatal(v, err)
	}
	for _, q := range []int64{0, -1, MaxQuantity + 1} {
		if _, err := ExitShortfallCeiling(m, q); err == nil {
			t.Fatal(q)
		}
	}
	m.FeeBPS = 1001
	if _, err := ExitShortfallCeiling(m, 1); err == nil {
		t.Fatal("unsupported model accepted")
	}
}
