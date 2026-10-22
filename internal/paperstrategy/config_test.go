package paperstrategy

import (
	"errors"
	"fmt"
	"reflect"
	"testing"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/domain"
	"git.subcult.tv/subculture-collective/mantik/internal/workerplan"
)

func definition() Definition {
	return Definition{ID: "baseline-v1", Method: Method, Symbol: "SPY", Feed: "iex", InputEvidenceClass: "prospective_market_unqualified", EntryAtOrBelowCents: 10000, Quantity: 10, Safety: domain.Safety{MaxNotionalCents: 100000, StopCents: 9500, TargetCents: 11000, MaxHoldingSeconds: 3600, MaxQuoteAgeSeconds: 60}}
}
func TestDefinitionSafetyAndNativePredicate(t *testing.T) {
	d := definition()
	for _, tc := range []struct {
		price int64
		want  bool
	}{{9999, true}, {10000, true}, {10001, false}} {
		got, err := Signal(d, tc.price)
		if err != nil || got != tc.want {
			t.Fatal(tc, got, err)
		}
	}
	for _, change := range []func(*Definition){func(d *Definition) { d.Method = "caller-code" }, func(d *Definition) { d.Symbol = "bad symbol" }, func(d *Definition) { d.Feed = "unknown" }, func(d *Definition) { d.InputEvidenceClass = "qualified" }, func(d *Definition) { d.EntryAtOrBelowCents = 0 }, func(d *Definition) { d.Quantity = 1 << 62 }, func(d *Definition) { d.Safety.MaxNotionalCents-- }, func(d *Definition) { d.Safety.StopCents = d.EntryAtOrBelowCents }, func(d *Definition) { d.Safety.TargetCents = d.EntryAtOrBelowCents }, func(d *Definition) { d.Safety.MaxHoldingSeconds = 0 }, func(d *Definition) { d.Safety.MaxQuoteAgeSeconds = 3601 }} {
		bad := d
		change(&bad)
		if bad.Validate() == nil {
			t.Fatal("invalid definition accepted", bad)
		}
		if _, err := Signal(bad, 10000); err == nil {
			t.Fatal("invalid configuration evaluated")
		}
	}
	if _, err := Signal(d, 0); err == nil {
		t.Fatal("missing price evaluated")
	}
}
func TestRecordsRetryCapacityAndNoAuthority(t *testing.T) {
	at := time.Date(2026, 10, 21, 12, 0, 0, 0, time.UTC)
	owner := "receiver-paperstrategy_test_01"
	c, _ := CatalogFor(owner, []Record{})
	noClock := func() time.Time { panic("retry/capacity read clock") }
	for i := 0; i < MaxDefinitions; i++ {
		d := definition()
		d.ID = fmt.Sprintf("strategy-%02d", i)
		r, err := Append(c, Command{owner, d}, func() time.Time { return at })
		if err != nil || r.Duplicate || r.Record.Validate() != nil || r.Record.AdmissionAvailable || r.Record.ExecutionAvailable || r.Record.QualificationCredit {
			t.Fatal(r, err)
		}
		c.Records = append(c.Records, r.Record)
	}
	selected := c.Records[0]
	r, err := Append(c, selected.Command, noClock)
	if err != nil || !r.Duplicate || !reflect.DeepEqual(r.Record, selected) {
		t.Fatal(r, err)
	}
	bad := selected.Command
	bad.Definition.Quantity--
	if _, err := Append(c, bad, noClock); !errors.Is(err, ErrConflict) {
		t.Fatal(err)
	}
	bad = selected.Command
	bad.Definition.ID = "overflow"
	if _, err := Append(c, bad, noClock); !errors.Is(err, ErrCapacity) {
		t.Fatal(err)
	}
	// Self-hashed flags cannot turn operator configuration into admission.
	c.Records[1].ExecutionAvailable = true
	c.Records[1].SHA256 = ""
	c.Records[1].SHA256 = workerplan.Hash(c.Records[1])
	if _, err := Select(c, selected.Command.Definition.ID); err == nil {
		t.Fatal("selected read hid unrelated damage")
	}
	if _, err := Append(c, selected.Command, noClock); err == nil {
		t.Fatal("retry hid unrelated damage")
	}
}
