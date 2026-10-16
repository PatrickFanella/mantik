package question

import (
	"encoding/json"
	"reflect"
	"regexp"
	"slices"
	"strings"

	"git.subcult.tv/subculture-collective/mantik/internal/news"
)

const InventorySchema = "mantik-review-hypothesis-inventory-v1"
const MaxDeclarations = 32

var receiver = regexp.MustCompile(`^receiver-[a-zA-Z0-9_-]{16,64}$`)

type Ref struct {
	ID                string `json:"id"`
	DeclarationSHA256 string `json:"declaration_sha256"`
}

type Summary struct {
	Ref
	EvidenceClass  string `json:"evidence_class"`
	Instrument     string `json:"instrument"`
	MechanismCode  string `json:"mechanism_code"`
	HorizonSeconds int64  `json:"horizon_seconds"`
}

// Inventory is built by the application from a validated retained catalogue.
// It carries declarations only; it does not assess their evidence or validity.
type Inventory struct {
	Schema          string   `json:"schema"`
	ReceiverID      string   `json:"receiver_id"`
	CatalogueSHA256 string   `json:"catalogue_sha256"`
	Declarations    []Result `json:"declarations"`
	SHA256          string   `json:"sha256"`
}

func MakeInventory(receiverID, catalogueSHA string, declarations []Result) (Inventory, error) {
	if !receiver.MatchString(receiverID) || !digest.MatchString(catalogueSHA) || len(declarations) > MaxDeclarations {
		return Inventory{}, ErrInvalid
	}
	i := Inventory{Schema: InventorySchema, ReceiverID: receiverID, CatalogueSHA256: catalogueSHA, Declarations: []Result{}}
	seen := map[Ref]bool{}
	for _, d := range declarations {
		expected, err := Seal(d.Declaration)
		if err != nil || !reflect.DeepEqual(expected, d) {
			return Inventory{}, ErrInvalid
		}
		ref := Ref{ID: d.ID, DeclarationSHA256: d.DeclarationSHA256}
		if !seen[ref] {
			i.Declarations = append(i.Declarations, expected)
		}
		seen[ref] = true
	}
	slices.SortFunc(i.Declarations, func(a, b Result) int {
		if c := strings.Compare(a.ID, b.ID); c != 0 {
			return c
		}
		return strings.Compare(a.DeclarationSHA256, b.DeclarationSHA256)
	})
	raw, _ := json.Marshal(i)
	i.SHA256 = news.SHA256(string(raw))
	return i, nil
}

func (i Inventory) Validate() error {
	expected, err := MakeInventory(i.ReceiverID, i.CatalogueSHA256, i.Declarations)
	if err != nil || !reflect.DeepEqual(expected, i) {
		return ErrInvalid
	}
	return nil
}

func (i Inventory) Refs() []Ref {
	refs := []Ref{}
	for _, d := range i.Declarations {
		refs = append(refs, Ref{ID: d.ID, DeclarationSHA256: d.DeclarationSHA256})
	}
	return refs
}

func (i Inventory) Summaries() []Summary {
	items := []Summary{}
	for _, d := range i.Declarations {
		items = append(items, Summary{Ref: Ref{ID: d.ID, DeclarationSHA256: d.DeclarationSHA256}, EvidenceClass: d.Declaration.EvidenceClass, Instrument: d.Declaration.Instrument, MechanismCode: d.Declaration.Mechanism.Code, HorizonSeconds: d.Declaration.HorizonSeconds})
	}
	return items
}
