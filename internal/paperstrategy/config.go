// Package paperstrategy retains explicit native paper strategy configuration.
// Registration is not admission, an evaluation result or an execution grant.
package paperstrategy

import (
	"errors"
	"reflect"
	"regexp"
	"slices"
	"strings"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/domain"
	"git.subcult.tv/subculture-collective/mantik/internal/paperaccount"
	"git.subcult.tv/subculture-collective/mantik/internal/workerplan"
)

const Method = "native-paper-threshold-v1"
const MaxDefinitions = 64
const MaxCommandBytes = 4096
const MaxRecordBytes = 8192
const MaxCatalogBytes = MaxDefinitions*MaxRecordBytes + 4096

var ErrInvalid = errors.New("invalid paper strategy definition")
var ErrConflict = errors.New("paper strategy ID already has a different definition")
var ErrCapacity = errors.New("paper strategy registry capacity reached")
var ErrNotFound = errors.New("paper strategy definition not found")
var receiver = regexp.MustCompile(`^receiver-[a-zA-Z0-9_-]{16,64}$`)
var symbol = regexp.MustCompile(`^[A-Z][A-Z0-9.-]{0,19}$`)

// Definition selects a compiled native method and declares safety and intended
// market-input provenance. It contains no account, balances, admission, model
// source or caller-supplied finalized decision. Input class is not attestation.
type Definition struct {
	ID                  string        `json:"id"`
	Method              string        `json:"method"`
	Symbol              string        `json:"symbol"`
	Feed                string        `json:"feed"`
	InputEvidenceClass  string        `json:"input_evidence_class"`
	EntryAtOrBelowCents int64         `json:"entry_at_or_below_cents"`
	Quantity            int64         `json:"quantity"`
	Safety              domain.Safety `json:"safety"`
}
type Command struct {
	ReceiverID string     `json:"receiver_id"`
	Definition Definition `json:"definition"`
}
type Record struct {
	Schema              string    `json:"schema"`
	Command             Command   `json:"command"`
	RegisteredAt        time.Time `json:"registered_at"`
	DefinitionSHA256    string    `json:"definition_sha256"`
	Authority           string    `json:"authority"`
	AdmissionAvailable  bool      `json:"admission_available"`
	ExecutionAvailable  bool      `json:"execution_available"`
	QualificationCredit bool      `json:"qualification_credit"`
	SHA256              string    `json:"sha256"`
}
type Receipt struct {
	Record    Record `json:"record"`
	Duplicate bool   `json:"duplicate"`
}
type Catalog struct {
	Schema     string   `json:"schema"`
	ReceiverID string   `json:"receiver_id"`
	Records    []Record `json:"records"`
}

func (d Definition) Validate() error {
	s := d.Safety
	if !paperaccount.ID.MatchString(d.ID) || d.Method != Method || !symbol.MatchString(d.Symbol) || !slices.Contains([]string{"iex", "sip", "delayed_sip"}, d.Feed) || !slices.Contains([]string{"synthetic_unqualified", "recorded_market_unqualified", "prospective_market_unqualified"}, d.InputEvidenceClass) || d.EntryAtOrBelowCents < 1 || d.EntryAtOrBelowCents > 100000000 || d.Quantity < 1 || s.MaxNotionalCents < 1 || s.MaxNotionalCents > paperaccount.MaxOpeningCashCents || d.Quantity > s.MaxNotionalCents/d.EntryAtOrBelowCents || s.StopCents < 1 || s.StopCents >= d.EntryAtOrBelowCents || s.TargetCents <= d.EntryAtOrBelowCents || s.TargetCents > 100000000 || s.MaxHoldingSeconds < 1 || s.MaxHoldingSeconds > 86400 || s.MaxQuoteAgeSeconds < 1 || s.MaxQuoteAgeSeconds > 3600 {
		return ErrInvalid
	}
	return nil
}
func (c Command) Validate() error {
	if !receiver.MatchString(c.ReceiverID) || c.Definition.Validate() != nil {
		return ErrInvalid
	}
	return nil
}
func New(c Command, at time.Time) (Record, error) {
	if c.Validate() != nil || at.IsZero() || at.UTC().Year() < 1 || at.UTC().Year() > 9999 {
		return Record{}, ErrInvalid
	}
	r := Record{Schema: "mantik-paper-strategy-definition-v1", Command: c, RegisteredAt: at.UTC(), DefinitionSHA256: workerplan.Hash(c.Definition), Authority: "operator_declared_paper_strategy_configuration_only"}
	r.SHA256 = workerplan.Hash(r)
	return r, nil
}
func (r Record) Validate() error {
	expected, err := New(r.Command, r.RegisteredAt)
	if err != nil || !reflect.DeepEqual(expected, r) {
		return ErrInvalid
	}
	return nil
}
func CatalogFor(owner string, all []Record) (Catalog, error) {
	if !receiver.MatchString(owner) || all == nil || len(all) > MaxDefinitions {
		return Catalog{}, ErrInvalid
	}
	c := Catalog{Schema: "mantik-paper-strategy-catalog-v1", ReceiverID: owner, Records: append([]Record{}, all...)}
	slices.SortFunc(c.Records, func(a, b Record) int { return strings.Compare(a.Command.Definition.ID, b.Command.Definition.ID) })
	for i, r := range c.Records {
		if r.Validate() != nil || r.Command.ReceiverID != owner || i > 0 && c.Records[i-1].Command.Definition.ID == r.Command.Definition.ID {
			return Catalog{}, ErrInvalid
		}
	}
	return c, nil
}
func (c Catalog) Validate() error {
	expected, err := CatalogFor(c.ReceiverID, c.Records)
	if err != nil || !reflect.DeepEqual(expected, c) {
		return ErrInvalid
	}
	return nil
}

// Select validates the complete catalogue before a selected read or retry.
func Select(c Catalog, id string) (Record, error) {
	if c.Validate() != nil || !paperaccount.ID.MatchString(id) {
		return Record{}, ErrInvalid
	}
	for _, r := range c.Records {
		if r.Command.Definition.ID == id {
			return r, nil
		}
	}
	return Record{}, ErrNotFound
}
func Append(c Catalog, cmd Command, now func() time.Time) (Receipt, error) {
	if c.Validate() != nil || cmd.Validate() != nil {
		return Receipt{}, ErrInvalid
	}
	if cmd.ReceiverID != c.ReceiverID {
		return Receipt{}, ErrConflict
	}
	r, err := Select(c, cmd.Definition.ID)
	if err == nil {
		if r.Command != cmd {
			return Receipt{}, ErrConflict
		}
		return Receipt{Record: r, Duplicate: true}, nil
	}
	if !errors.Is(err, ErrNotFound) {
		return Receipt{}, err
	}
	if len(c.Records) >= MaxDefinitions {
		return Receipt{}, ErrCapacity
	}
	if now == nil {
		return Receipt{}, ErrInvalid
	}
	r, err = New(cmd, now())
	return Receipt{Record: r}, err
}

// Signal applies the versioned native threshold predicate only. Provenance,
// freshness, calendar, admission and allocation remain separate owner checks.
func Signal(d Definition, priceCents int64) (bool, error) {
	if d.Validate() != nil || priceCents < 1 || priceCents > 100000000 {
		return false, ErrInvalid
	}
	return priceCents <= d.EntryAtOrBelowCents, nil
}
