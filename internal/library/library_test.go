package library

import (
	"testing"

	"git.subcult.tv/subculture-collective/mantik/internal/domain"
)

func TestInvalidContracts(t *testing.T) {
	for _, name := range []string{"version", "input", "instrument", "output", "evidence", "safety", "recipe", "check", "dependency", "cycle", "duplicate"} {
		t.Run(name, func(t *testing.T) {
			e := FixtureEntry()
			switch name {
			case "version":
				e.ContractVersion = "v2"
			case "input":
				e.Inputs[0].Schema = ""
			case "instrument":
				e.Instruments = nil
			case "output":
				e.Outputs = append(e.Outputs, "allocate")
			case "evidence":
				e.EvidenceClass = "qualified"
			case "safety":
				e.SafetyRequirements = e.SafetyRequirements[1:]
			case "recipe":
				e.Evaluation.Required = nil
				e.Evaluation.OptionalTools = []string{"quote_freshness"}
			case "check":
				e.Evaluation.Required[0].Version = ""
			case "dependency":
				e.Dependencies = []string{"unknown-v1"}
			case "cycle":
				e.Dependencies = []string{e.ID}
			case "duplicate":
				if _, err := New(e, e); err == nil {
					t.Fatal("duplicate version admitted")
				}
				return
			}
			if _, err := New(e); err == nil {
				t.Fatal("invalid library admitted")
			}
		})
	}
}

func TestRegistryCompatibilityAndIsolation(t *testing.T) {
	e := FixtureEntry()
	component := FixtureEntry()
	component.ID, component.Kind = "quote-component-v1", "component"
	e.Dependencies = []string{component.ID}
	r, err := New(e, component)
	if err != nil {
		t.Fatal(err)
	}
	e.Evaluation.Required[0].Actions[0] = "corrupted"
	e.Inputs[0].Schema = "corrupted"
	resolved, err := r.Resolve(e.ID, ContractVersion)
	if err != nil {
		t.Fatal(err)
	}
	resolved.Evaluation.Required[0].Actions[0] = "also-corrupted"
	again, _ := r.Resolve(e.ID, ContractVersion)
	if again.Inputs[0].Schema != "observation-quote-cents-v1" || again.Evaluation.Required[0].Actions[0] != "enter" {
		t.Fatal("registry mutated through external slices")
	}
	for _, request := range [][2]string{{e.ID, "v2"}, {"demo-threshold-v2", ContractVersion}} {
		if _, err := r.Resolve(request[0], request[1]); err == nil {
			t.Fatal("incompatible version resolved")
		}
	}
}

func TestAdmission(t *testing.T) {
	e := FixtureEntry()
	results := []domain.EvaluationResult{
		{Check: "quote_freshness", Version: "v1", Passed: true},
		{Check: "event_order", Version: "v1", Passed: true},
		{Check: "notional_limit", Version: "v1", Passed: true},
		{Check: "protective_levels", Version: "v1", Passed: true},
	}
	if err := e.Admit("enter", results); err != nil {
		t.Fatal(err)
	}
	if err := e.Admit("exit", results[:2]); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"missing", "failed", "duplicate", "version", "tool", "action"} {
		t.Run(name, func(t *testing.T) {
			r := append([]domain.EvaluationResult(nil), results...)
			action := "enter"
			switch name {
			case "missing":
				r = r[:3]
			case "failed":
				r[0].Passed = false
			case "duplicate":
				r = append(r, r[0])
			case "version":
				r[0].Version = "v2"
			case "tool":
				r[0].Check = "optional_search"
			case "action":
				action = "allocate"
			}
			if err := e.Admit(action, r); err == nil {
				t.Fatal("inadmissible action passed")
			}
		})
	}
	for i := range results {
		results[i].Version = ""
	}
	if err := e.Admit("enter", results); err != nil {
		t.Fatalf("retained bootstrap v1 result rejected: %v", err)
	}
}

func TestImmutableFixture(t *testing.T) {
	if _, err := ResolveFixture(FixtureStrategy()); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"id", "quantity", "threshold", "safety", "evidence"} {
		t.Run(name, func(t *testing.T) {
			s := FixtureStrategy()
			switch name {
			case "id":
				s.ID = "demo-threshold-v2"
			case "quantity":
				s.Quantity++
			case "threshold":
				s.EntryAtOrBelowCents++
			case "safety":
				s.Safety.MaxHoldingSeconds = 0
			case "evidence":
				s.Hypothesis.EvidenceClass = "prospective"
			}
			if _, err := ResolveFixture(s); err == nil {
				t.Fatal("changed immutable fixture admitted")
			}
		})
	}
}
