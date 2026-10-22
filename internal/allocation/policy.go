package allocation

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"regexp"
	"slices"
	"strings"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/paperaccount"
	"git.subcult.tv/subculture-collective/mantik/internal/paperfill"
	"git.subcult.tv/subculture-collective/mantik/internal/workerplan"
)

const MaxPolicyBytes = 128 << 10
const MaxPolicyRecordBytes = MaxPolicyBytes + 4096
const MaxPolicyCatalogBytes = paperaccount.MaxAccounts*MaxPolicyRecordBytes + 4096

var ErrPolicyConflict = errors.New("allocation policy ID or account already has different ownership")
var ErrPolicyNotFound = errors.New("allocation policy or funding account not found")
var receiverPattern = regexp.MustCompile(`^receiver-[a-zA-Z0-9_-]{16,64}$`)

type PolicyCommand struct {
	ReceiverID     string           `json:"receiver_id"`
	AccountID      string           `json:"account_id"`
	Policy         Policy           `json:"policy"`
	ExecutionModel *paperfill.Model `json:"execution_model,omitempty"`
}
type PolicyRecord struct {
	Schema                string               `json:"schema"`
	Command               PolicyCommand        `json:"command"`
	Account               paperaccount.Account `json:"account"`
	RegisteredAt          time.Time            `json:"registered_at"`
	Authority             string               `json:"authority"`
	ReservationsAvailable bool                 `json:"reservations_available"`
	QualificationCredit   bool                 `json:"qualification_credit"`
	SHA256                string               `json:"sha256"`
}
type PolicyReceipt struct {
	Record    PolicyRecord `json:"record"`
	Duplicate bool         `json:"duplicate"`
}
type PolicyCatalog struct {
	Schema     string         `json:"schema"`
	ReceiverID string         `json:"receiver_id"`
	Records    []PolicyRecord `json:"records"`
}

// ValidateFor checks the complete policy against immutable funding without
// supplying any admission or exposure. It cannot qualify or allocate a strategy.
func (p Policy) ValidateFor(a paperaccount.Account) error {
	raw, err := json.Marshal(p)
	if err != nil || len(raw) > MaxPolicyBytes {
		return ErrInvalid
	}
	s := Snapshot{Account: a, AsOf: a.CreatedAt, AvailableCashCents: a.Config.OpeningCashCents, Positions: []Exposure{}, Orders: []Exposure{}, Admissions: []Admission{}, Commitments: []Commitment{}}
	_, err = Evaluate(context.Background(), p, s, []Proposal{}, a.CreatedAt)
	return err
}
func (c PolicyCommand) Validate() error {
	if c.ExecutionModel != nil && c.ExecutionModel.Validate() != nil {
		return ErrInvalid
	}
	if !receiverPattern.MatchString(c.ReceiverID) || !id(c.AccountID) || !id(c.Policy.ID) || !validHash(c.Policy.AccountSHA256) {
		return ErrInvalid
	}
	return nil
}
func NewPolicyRecord(c PolicyCommand, a paperaccount.Account, at time.Time) (PolicyRecord, error) {
	if c.Validate() != nil || a.Validate() != nil || c.ReceiverID != a.ReceiverID || c.AccountID != a.Config.ID || c.Policy.ValidateFor(a) != nil || !validTime(at) || at.Before(a.CreatedAt) {
		return PolicyRecord{}, ErrInvalid
	}
	// Detach nested policy slices from the command's caller.
	raw, _ := json.Marshal(c)
	var owned PolicyCommand
	_ = json.Unmarshal(raw, &owned)
	r := PolicyRecord{Schema: "mantik-owned-allocation-policy-v1", Command: owned, Account: a, RegisteredAt: at.UTC(), Authority: "operator_declared_allocation_policy_only"}
	if owned.ExecutionModel != nil {
		r.Schema = "mantik-owned-allocation-policy-v2"
	}
	r.SHA256 = workerplan.Hash(r)
	return r, nil
}
func (r PolicyRecord) Validate() error {
	expected, err := NewPolicyRecord(r.Command, r.Account, r.RegisteredAt)
	if err != nil || !reflect.DeepEqual(expected, r) {
		return ErrInvalid
	}
	return nil
}

// PoliciesFor checks the full account/policy prefix. A selected read or retry
// cannot hide another corrupt policy, duplicate account owner or missing funding.
func PoliciesFor(accounts paperaccount.Catalog, records []PolicyRecord) (PolicyCatalog, error) {
	if accounts.Validate() != nil || records == nil || len(records) > paperaccount.MaxAccounts {
		return PolicyCatalog{}, ErrInvalid
	}
	all := make([]PolicyRecord, 0, len(records))
	seenID := map[string]bool{}
	seenAccount := map[string]bool{}
	for _, r := range records {
		if r.Validate() != nil || r.Command.ReceiverID != accounts.ReceiverID || seenID[r.Command.Policy.ID] || seenAccount[r.Command.AccountID] {
			return PolicyCatalog{}, ErrInvalid
		}
		found := false
		for _, a := range accounts.Accounts {
			if a.Config.ID == r.Command.AccountID && reflect.DeepEqual(a, r.Account) {
				found = true
				break
			}
		}
		if !found {
			return PolicyCatalog{}, ErrInvalid
		}
		seenID[r.Command.Policy.ID] = true
		seenAccount[r.Command.AccountID] = true
		owned, _ := NewPolicyRecord(r.Command, r.Account, r.RegisteredAt)
		all = append(all, owned)
	}
	slices.SortFunc(all, func(a, b PolicyRecord) int { return strings.Compare(a.Command.Policy.ID, b.Command.Policy.ID) })
	return PolicyCatalog{Schema: "mantik-owned-allocation-policy-catalog-v1", ReceiverID: accounts.ReceiverID, Records: all}, nil
}
func (c PolicyCatalog) Validate() error {
	accounts := []paperaccount.Account{}
	for _, r := range c.Records {
		accounts = append(accounts, r.Account)
	}
	a, err := paperaccount.MakeCatalog(c.ReceiverID, accounts)
	if err != nil {
		return ErrInvalid
	}
	expected, err := PoliciesFor(a, c.Records)
	if err != nil || !reflect.DeepEqual(expected, c) {
		return ErrInvalid
	}
	return nil
}
