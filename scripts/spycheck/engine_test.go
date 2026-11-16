package main

import (
	"encoding/json"
	"testing"

	"git.subcult.tv/subculture-collective/mantik/internal/engine"
)

func TestOracleDollarMismatchCannotPassOnMatchingReturns(t *testing.T) {
	r := engine.Result{Days: []engine.Day{{Date: "2016-01-05", Equity: "2500", TWR: "0"}}}
	ref := oracleRun{FinalEquity: "2500", TWR: "0"}
	if _, e := compareOracleEnd(r, ref); e != nil {
		t.Fatal(e)
	}
	ref.FinalEquity = "2000"
	if _, e := compareOracleEnd(r, ref); e == nil {
		t.Fatal("dropped deposit escaped oracle check")
	}
	ref.FinalEquity = "2500"
	ref.TWR = "0.01"
	if _, e := compareOracleEnd(r, ref); e == nil {
		t.Fatal("changed final TWR escaped oracle check")
	}
}
func TestOracleRequiresExactInputIdentity(t *testing.T) {
	raw, e := json.Marshal(map[string]any{"version": "spy-next-open-decimal-receivables-v2", "input_sha256": "wrong", "initial_cash": "2000", "deposit_amount": "500", "every_weeks": 2})
	if e != nil {
		t.Fatal(e)
	}
	if _, e = compareEngine(comparisonInput{}, []byte("input"), raw); e == nil {
		t.Fatal("unbound oracle accepted")
	}
}
