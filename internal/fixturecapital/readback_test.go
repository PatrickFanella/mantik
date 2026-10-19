package fixturecapital_test

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"math"
	"testing"

	"git.subcult.tv/subculture-collective/mantik/internal/fixturecapital"
)

func TestReadbackValidationDoesNotTrustRehashedBalances(t *testing.T) {
	h := setup(t, 100000, 100000)
	h.reserve(t)
	valid := snapshot(t, h.book)
	if err := valid.Validate(h.account); err != nil {
		t.Fatal(err)
	}
	for _, mutate := range []func(*fixturecapital.Snapshot){
		func(s *fixturecapital.Snapshot) { s.AvailableCashCents = math.MaxInt64 },
		func(s *fixturecapital.Snapshot) { s.AvailableCashCents++ },
		func(s *fixturecapital.Snapshot) { s.QualificationCredit = true },
		func(s *fixturecapital.Snapshot) { s.ReservedCashCents = 0; s.AvailableCashCents = 100000 },
		func(s *fixturecapital.Snapshot) { s.Commitments = nil },
		func(s *fixturecapital.Snapshot) { s.AccountID = "elsewhere" },
	} {
		raw, _ := json.Marshal(valid)
		var bad fixturecapital.Snapshot
		_ = json.Unmarshal(raw, &bad)
		mutate(&bad)
		bad.SHA256 = ""
		raw, _ = json.Marshal(bad)
		bad.SHA256 = fmt.Sprintf("%x", sha256.Sum256(raw))
		if err := bad.Validate(h.account); err == nil {
			t.Fatal("rehashing bypassed readback contract", bad)
		}
	}
}
