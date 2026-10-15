package experiment

import (
	"encoding/json"
	"reflect"
	"testing"

	"git.subcult.tv/subculture-collective/mantik/internal/library"
	"git.subcult.tv/subculture-collective/mantik/internal/replay"
)

func TestBuiltinCatalogPinsAndCopyIsolation(t *testing.T) {
	c, err := BuiltinCatalog()
	if err != nil {
		t.Fatal(err)
	}
	if c.Schema != CatalogSchema || c.ContractVersion != library.ContractVersion || c.Scope != "compiled_synthetic_methods" || c.QualificationCredit || len(c.Methods) != 4 {
		t.Fatalf("catalogue scope: %+v", c)
	}
	registry, err := Registry()
	if err != nil {
		t.Fatal(err)
	}
	for _, pin := range c.Methods {
		e, err := registry.Resolve(pin.ID, c.ContractVersion)
		if err != nil || !reflect.DeepEqual(e, pin.Definition.Entry) {
			t.Fatal("catalogue differs from registry")
		}
		hash, err := digest(pin.Definition)
		if err != nil || hash != pin.SHA256 || pin.Definition.Entry.EvidenceClass != "synthetic_unqualified" {
			t.Fatal("pin or evidence class changed")
		}
	}
	d, err := replay.FixtureDataset()
	if err != nil {
		t.Fatal(err)
	}
	recipe, err := DefaultRecipe(d)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(c.Methods[:2], recipe.Methods) || !reflect.DeepEqual(c.Methods[2:], recipe.Benchmarks) {
		t.Fatal("recipe method pins diverged")
	}
	c.Methods[0].Definition.Entry.Evaluation.Required[0].ID = "mutated"
	c.Methods[0].Definition.Assumptions[0] = "mutated"
	again, err := BuiltinCatalog()
	if err != nil || again.Methods[0].Definition.Entry.Evaluation.Required[0].ID == "mutated" || again.Methods[0].Definition.Assumptions[0] == "mutated" {
		t.Fatal("readback mutated compiled definitions")
	}
}

func TestCatalogRejectsChangedScopeAndPins(t *testing.T) {
	original, err := BuiltinCatalog()
	if err != nil {
		t.Fatal(err)
	}
	for _, change := range []func(*Catalog){
		func(c *Catalog) { c.Schema = "unknown" }, func(c *Catalog) { c.QualificationCredit = true },
		func(c *Catalog) { c.Methods[0].Definition.Safety.StopCents++ },
		func(c *Catalog) { c.Methods[0].ID = "changed" },
		func(c *Catalog) { c.Methods = append(c.Methods, c.Methods[0]) },
		func(c *Catalog) { c.Methods = nil },
	} {
		raw, _ := json.Marshal(original)
		var c Catalog
		_ = json.Unmarshal(raw, &c)
		change(&c)
		if err := c.Validate(); err == nil {
			t.Fatal("changed scope or definition accepted")
		}
	}
}
