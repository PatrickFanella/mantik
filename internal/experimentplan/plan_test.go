package experimentplan

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func declaredPlan(t *testing.T) Recipe {
	t.Helper()
	c, err := BuiltinCatalog()
	if err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 1, 5, 15, 0, 0, 0, time.UTC)
	r, err := Seal(Recipe{Schema: RecipeSchema, ID: "declared-plan", Version: "v1", DatasetID: "declared-data", DatasetVersion: "v1", DatasetSHA256: strings.Repeat("a", 64), WindowStart: at, WindowEnd: at.Add(time.Minute), Cutoffs: []time.Time{at, at.Add(time.Minute)}, RequiredKeys: []string{"first", "last"}, Methods: c.Methods[:2], Benchmarks: c.Methods[2:], CapitalCents: 100000, CostModel: CostModel, PlannedMetrics: Metrics()})
	if err != nil {
		t.Fatal(err)
	}
	return r
}
func TestPlanValidationWithoutExecution(t *testing.T) {
	r := declaredPlan(t)
	raw, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := ReadRecipe(strings.NewReader(string(raw)))
	if err != nil || decoded.SHA256 != r.SHA256 {
		t.Fatal(err)
	}
	// A plan binds a declared dataset identity; it does not load or admit data.
	if decoded.DatasetID != "declared-data" {
		t.Fatal("dataset declaration changed")
	}
	for _, name := range []string{"zero", "duplicate", "backwards", "out-of-window"} {
		t.Run(name, func(t *testing.T) {
			changed := declaredPlan(t)
			switch name {
			case "zero":
				changed.Cutoffs[0] = time.Time{}
			case "duplicate":
				changed.Cutoffs = []time.Time{changed.WindowStart, changed.WindowStart, changed.WindowEnd}
			case "backwards":
				changed.Cutoffs = []time.Time{changed.WindowStart, changed.WindowStart.Add(40 * time.Second), changed.WindowStart.Add(20 * time.Second), changed.WindowEnd}
			case "out-of-window":
				changed.Cutoffs[1] = changed.WindowEnd.Add(time.Second)
			}
			if _, err := Seal(changed); err == nil {
				t.Fatal("invalid cutoff declaration accepted")
			}
		})
	}
}
func TestPlanCopiesAndExactNativePins(t *testing.T) {
	r := declaredPlan(t)
	sealed, err := Seal(r)
	if err != nil {
		t.Fatal(err)
	}
	r.Methods[0].Definition.Strategy.Quantity++
	r.Methods[0].Definition.Assumptions[0] = "mutated"
	r.RequiredKeys[0] = "mutated"
	if err = sealed.Validate(); err != nil {
		t.Fatal("caller mutation reached sealed plan", err)
	}
	if _, err = Seal(r); err == nil {
		t.Fatal("changed method configuration admitted")
	}
	metrics := Metrics()
	metrics[0] = "mutated"
	if Metrics()[0] != "observations" {
		t.Fatal("caller changed metric contract")
	}
}
