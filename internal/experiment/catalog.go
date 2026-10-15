package experiment

import (
	"errors"
	"git.subcult.tv/subculture-collective/mantik/internal/library"
)

const CatalogSchema = "mantik-method-catalog-v1"

// Catalog describes compiled methods, not deployed strategies or qualification.
type Catalog struct {
	Schema              string      `json:"schema"`
	ContractVersion     string      `json:"contract_version"`
	Scope               string      `json:"scope"`
	QualificationCredit bool        `json:"qualification_credit"`
	Methods             []MethodPin `json:"methods"`
}

// Validate checks a readback's declared schema, bounded synthetic scope and
// content hashes. It does not bind unknown methods to local executable code.
func (c Catalog) Validate() error {
	if c.Schema != CatalogSchema || c.ContractVersion != library.ContractVersion || c.Scope != "compiled_synthetic_methods" || c.QualificationCredit || len(c.Methods) < 1 || len(c.Methods) > 16 {
		return errors.New("incompatible compiled method catalogue")
	}
	entries := []library.Entry{}
	for _, pin := range c.Methods {
		hash, err := digest(pin.Definition)
		if err != nil || pin.ID != pin.Definition.Entry.ID || pin.SHA256 != hash || pin.Definition.Entry.EvidenceClass != "synthetic_unqualified" {
			return errors.New("compiled method pin does not match its synthetic definition")
		}
		entries = append(entries, pin.Definition.Entry)
	}
	_, err := library.New(entries...)
	return err
}

func builtinMethodIDs() []string {
	return []string{library.FixtureStrategy().ID, library.HalfSizeID, NoActionID, PassiveID}
}

func BuiltinCatalog() (Catalog, error) {
	c := Catalog{Schema: CatalogSchema, ContractVersion: library.ContractVersion, Scope: "compiled_synthetic_methods", Methods: []MethodPin{}}
	if _, err := Registry(); err != nil {
		return c, err
	}
	for _, id := range builtinMethodIDs() {
		m, err := MethodByID(id)
		if err != nil {
			return c, err
		}
		hash, err := digest(m)
		if err != nil {
			return c, err
		}
		c.Methods = append(c.Methods, MethodPin{ID: id, SHA256: hash, Definition: m})
	}
	return c, c.Validate()
}
