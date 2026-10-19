// Package paperaccount retains operator-declared paper capital configuration.
// It grants no allocation, execution, broker access or qualification authority.
package paperaccount

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"reflect"
	"regexp"
	"slices"
	"time"
)

const Schema = "mantik-paper-account-v1"
const CatalogSchema = "mantik-paper-account-catalog-v1"
const MaxAccounts = 16
const MaxOpeningCashCents int64 = 1_000_000_000_000

var ID = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,128}$`)
var receiverID = regexp.MustCompile(`^receiver-[a-zA-Z0-9_-]{16,64}$`)
var ErrInvalid = errors.New("invalid paper account configuration or retained receipt")
var ErrConflict = errors.New("paper account ID already has different capital configuration")
var ErrCapacity = errors.New("paper account capacity reached")
var ErrNotFound = errors.New("paper account not found")

type Config struct {
	ID                string `json:"id"`
	Currency          string `json:"currency"`
	OpeningCashCents  int64  `json:"opening_cash_cents"`
	CapitalLimitCents int64  `json:"capital_limit_cents"`
}
type Funding struct {
	ID          string `json:"id"`
	Kind        string `json:"kind"`
	AmountCents int64  `json:"amount_cents"`
	Currency    string `json:"currency"`
}
type Account struct {
	Schema              string    `json:"schema"`
	ReceiverID          string    `json:"receiver_id"`
	Config              Config    `json:"config"`
	CreatedAt           time.Time `json:"created_at"`
	OpeningFunding      Funding   `json:"opening_funding"`
	Policy              string    `json:"policy"`
	Authority           string    `json:"authority"`
	AllocationAvailable bool      `json:"allocation_available"`
	QualificationCredit bool      `json:"qualification_credit"`
	SHA256              string    `json:"sha256"`
}
type Receipt struct {
	Account   Account `json:"account"`
	Duplicate bool    `json:"duplicate"`
}
type Catalog struct {
	Schema     string    `json:"schema"`
	ReceiverID string    `json:"receiver_id"`
	Accounts   []Account `json:"accounts"`
}

func digest(v any) string {
	raw, _ := json.Marshal(v)
	h := sha256.Sum256(raw)
	return hex.EncodeToString(h[:])
}
func (c Config) Validate() error {
	if !ID.MatchString(c.ID) || c.Currency != "USD" || c.OpeningCashCents < 1 || c.OpeningCashCents > MaxOpeningCashCents || c.CapitalLimitCents < 1 || c.CapitalLimitCents > c.OpeningCashCents {
		return ErrInvalid
	}
	return nil
}
func Create(receiver string, c Config, at time.Time) (Account, error) {
	if !receiverID.MatchString(receiver) || c.Validate() != nil || at.IsZero() || at.UTC().Year() < 1 || at.UTC().Year() > 9999 {
		return Account{}, ErrInvalid
	}
	a := Account{Schema: Schema, ReceiverID: receiver, Config: c, CreatedAt: at.UTC(), Policy: "cash_only_no_leverage_v1", Authority: "operator_declared_paper_capital_only"}
	a.OpeningFunding = Funding{ID: "paper-funding-" + digest(struct {
		ReceiverID string
		AccountID  string
	}{receiver, c.ID}), Kind: "opening_paper_funding", AmountCents: c.OpeningCashCents, Currency: c.Currency}
	a.SHA256 = digest(a)
	return a, nil
}
func (a Account) Validate() error {
	expected, err := Create(a.ReceiverID, a.Config, a.CreatedAt)
	if err != nil || !reflect.DeepEqual(expected, a) {
		return ErrInvalid
	}
	return nil
}
func MakeCatalog(receiver string, all []Account) (Catalog, error) {
	if !receiverID.MatchString(receiver) || len(all) > MaxAccounts {
		return Catalog{}, ErrInvalid
	}
	c := Catalog{Schema: CatalogSchema, ReceiverID: receiver, Accounts: append([]Account{}, all...)}
	slices.SortFunc(c.Accounts, func(a, b Account) int {
		if a.Config.ID < b.Config.ID {
			return -1
		}
		if a.Config.ID > b.Config.ID {
			return 1
		}
		return 0
	})
	for i, a := range c.Accounts {
		if a.Validate() != nil || a.ReceiverID != receiver || i > 0 && c.Accounts[i-1].Config.ID == a.Config.ID {
			return Catalog{}, ErrInvalid
		}
	}
	return c, nil
}
func (c Catalog) Validate() error {
	expected, err := MakeCatalog(c.ReceiverID, c.Accounts)
	if err != nil || !reflect.DeepEqual(expected, c) {
		return ErrInvalid
	}
	return nil
}
