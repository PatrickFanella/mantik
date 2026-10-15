package experiment

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"git.subcult.tv/subculture-collective/mantik/internal/experimentplan"
)

// These identities were captured from the pre-extraction v1 implementation.
// A contract version change needs a new identity rather than updating these pins.
func TestV1DeclarationWireCompatibility(t *testing.T) {
	r, _ := fixture(t)
	if r.SHA256 != "9da1c82a0bdbdab99dcf61dd3c59cb2b70b75482639b48f953063cb7752a7d8e" {
		t.Fatal("existing v1 recipe identity changed", r.SHA256)
	}
	expected := map[string]string{
		"demo-threshold-v1":       "8ad55599dc888d669c37c37ea980ef04da0e55c227c46d9809217b55a4c82fe9",
		"demo-half-size-v1":       "ea355ec02d8796c44db684f825356e0875390c3f2e6d97221e15e70c7fcfa88b",
		"no-action-control-v1":    "af548a08f3b1a5a52248e1a08d4fdbb235c2599958f972e130236e560a7c4481",
		"passive-mark-control-v1": "04c89f2c47c6c0cc1e80466c5eeb676ba6623a66532e7ccd71ae7ccfd49c1ebd",
	}
	catalog, err := BuiltinCatalog()
	if err != nil {
		t.Fatal(err)
	}
	shared, err := experimentplan.BuiltinCatalog()
	if err != nil || !reflect.DeepEqual(catalog, shared) {
		t.Fatal("runner and validation catalogues differ", err)
	}
	for _, pin := range catalog.Methods {
		if expected[pin.ID] != pin.SHA256 {
			t.Fatal("existing v1 method identity changed", pin.ID, pin.SHA256)
		}
	}
	raw, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := experimentplan.ReadRecipe(strings.NewReader(string(raw)))
	if err != nil || !reflect.DeepEqual(decoded, r) {
		t.Fatal("shared reader changed wire declaration", err)
	}
	resealed, err := experimentplan.Seal(decoded)
	if err != nil || !reflect.DeepEqual(resealed, r) {
		t.Fatal("shared sealing changed declaration", err)
	}
}
