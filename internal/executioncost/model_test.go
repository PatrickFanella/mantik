package executioncost

import (
	"math/big"
	"testing"
)

func TestFrozenDeclarationAndForwardOnlyDefault(t *testing.T) {
	for _, raw := range [][]byte{nil, []byte("null"), []byte("  null ")} {
		if got, e := FromDeclaration(raw, true); e != nil || got != Default() {
			t.Fatal(got, e)
		}
		if _, e := FromDeclaration(raw, false); e == nil {
			t.Fatal("historical declaration silently defaulted")
		}
	}
	for _, raw := range []string{`{}`, `{"version":"v1","fee_bps":"0"}`, `{"version":"v1","fee_bps":"10000","slippage_bps":"0"}`} {
		if _, e := FromDeclaration([]byte(raw), true); e == nil {
			t.Fatal("partial/invalid costs accepted", raw)
		}
	}
	got, e := FromDeclaration([]byte(`{"version":"frozen-v1","fee_bps":"1","slippage_bps":"5"}`), false)
	if e != nil {
		t.Fatal(e)
	}
	fee, slip, e := got.Parse()
	if e != nil || fee.Cmp(big.NewRat(1, 10000)) != 0 || slip.Cmp(big.NewRat(5, 10000)) != 0 {
		t.Fatal(fee, slip, e)
	}
	ref := big.NewRat(100, 1)
	if Price(ref, slip, true).Cmp(big.NewRat(10005, 100)) != 0 || Price(ref, slip, false).Cmp(big.NewRat(9995, 100)) != 0 || ref.Cmp(big.NewRat(100, 1)) != 0 {
		t.Fatal("adverse price or source mutation")
	}
}
