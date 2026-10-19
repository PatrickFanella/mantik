package paperledger

import (
	"context"
	"reflect"
	"regexp"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/paperaccount"
)

const RehearsalSchema = "mantik-paper-ledger-rehearsal-v1"
const RehearsalScope = "record_synthetic_ledger_rehearsal_v1"
const MaxRehearsalBytes = 1 << 20

var sha256Pattern = regexp.MustCompile(`^[a-f0-9]{64}$`)

// AppendCommand is a synthetic accounting assertion. It is not a proposal or
// authenticated economic effect and cannot carry execution authority.
type AppendCommand struct {
	ReceiverID         string `json:"receiver_id"`
	AccountSHA256      string `json:"account_sha256"`
	ExpectedHeadSHA256 string `json:"expected_head_sha256"`
	ScopeCode          string `json:"scope_code"`
	Entry              Entry  `json:"entry"`
}
type Accepted struct {
	Command      AppendCommand `json:"command"`
	AcceptedAt   time.Time     `json:"accepted_at"`
	Receipt      Receipt       `json:"receipt"`
	ResultSHA256 string        `json:"result_sha256"`
}
type Rehearsal struct {
	Schema              string               `json:"schema"`
	Account             paperaccount.Account `json:"account"`
	Accepted            []Accepted           `json:"accepted"`
	Result              Result               `json:"result"`
	Authority           string               `json:"authority"`
	AllocationAvailable bool                 `json:"allocation_available"`
	ExecutionAttested   bool                 `json:"execution_attested"`
	QualificationCredit bool                 `json:"qualification_credit"`
	SHA256              string               `json:"sha256"`
}
type AppendReceipt struct {
	AccountID     string   `json:"account_id"`
	AccountSHA256 string   `json:"account_sha256"`
	Accepted      Accepted `json:"accepted"`
	Duplicate     bool     `json:"duplicate"`
}

func (c AppendCommand) Validate() error {
	if !sha256Pattern.MatchString(c.AccountSHA256) || !sha256Pattern.MatchString(c.ExpectedHeadSHA256) || c.ScopeCode != RehearsalScope || !validEntry(c.Entry) {
		return ErrInvalid
	}
	return nil
}
func NewRehearsal(ctx context.Context, a paperaccount.Account) (Rehearsal, error) {
	result, err := Audit(ctx, Recording{Schema: RecordingSchema, EvidenceClass: "synthetic_unqualified", Account: a, Entries: []Entry{}})
	if err != nil {
		return Rehearsal{}, err
	}
	r := Rehearsal{Schema: RehearsalSchema, Account: a, Accepted: []Accepted{}, Result: result, Authority: "service_retained_synthetic_ledger_rehearsal_only"}
	r.SHA256 = digest(r)
	return r, nil
}
func sameCommand(a, b AppendCommand) bool {
	a.Entry.OccurredAt = a.Entry.OccurredAt.UTC()
	b.Entry.OccurredAt = b.Entry.OccurredAt.UTC()
	return a == b
}

// appendChecked assumes the existing prefix has been validated. Validation
// replays prefixes with this helper so stored receipts and compare-and-swap pins
// are checked against the same transition contract as new commands.
func appendChecked(ctx context.Context, r Rehearsal, c AppendCommand, at time.Time) (Rehearsal, AppendReceipt, error) {
	if c.Validate() != nil || c.ReceiverID != r.Account.ReceiverID || c.AccountSHA256 != r.Account.SHA256 {
		return Rehearsal{}, AppendReceipt{}, ErrInvalid
	}
	c.Entry.OccurredAt = c.Entry.OccurredAt.UTC()
	for _, old := range r.Accepted {
		if old.Command.Entry.ID == c.Entry.ID {
			if !sameCommand(old.Command, c) {
				return Rehearsal{}, AppendReceipt{}, ErrConflict
			}
			return r, AppendReceipt{AccountID: r.Account.Config.ID, AccountSHA256: r.Account.SHA256, Accepted: old, Duplicate: true}, nil
		}
	}
	if c.ExpectedHeadSHA256 != r.Result.LedgerHeadSHA256 {
		return Rehearsal{}, AppendReceipt{}, ErrConflict
	}
	if len(r.Accepted) >= MaxEntries {
		return Rehearsal{}, AppendReceipt{}, ErrLimit
	}
	if !validTime(at) || at.Before(r.Account.CreatedAt) || c.Entry.OccurredAt.After(at) || len(r.Accepted) > 0 && at.Before(r.Accepted[len(r.Accepted)-1].AcceptedAt) {
		return Rehearsal{}, AppendReceipt{}, ErrConflict
	}
	entries := make([]Entry, 0, len(r.Accepted)+1)
	for _, old := range r.Accepted {
		entries = append(entries, old.Command.Entry)
	}
	entries = append(entries, c.Entry)
	result, err := Audit(ctx, Recording{Schema: RecordingSchema, EvidenceClass: "synthetic_unqualified", Account: r.Account, Entries: entries})
	if err != nil {
		return Rehearsal{}, AppendReceipt{}, err
	}
	accepted := Accepted{Command: c, AcceptedAt: at.UTC(), Receipt: result.Receipts[len(result.Receipts)-1], ResultSHA256: result.SHA256}
	r.Accepted = append(append([]Accepted{}, r.Accepted...), accepted)
	r.Result = result
	r.SHA256 = ""
	r.SHA256 = digest(r)
	return r, AppendReceipt{AccountID: r.Account.Config.ID, AccountSHA256: r.Account.SHA256, Accepted: accepted}, nil
}
func (r Rehearsal) Validate(ctx context.Context) error {
	if r.Accepted == nil || len(r.Accepted) > MaxEntries {
		return ErrInvalid
	}
	expected, err := NewRehearsal(ctx, r.Account)
	if err != nil {
		return err
	}
	for _, old := range r.Accepted {
		next, receipt, err := appendChecked(ctx, expected, old.Command, old.AcceptedAt)
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return ErrInvalid
		}
		if receipt.Duplicate || !reflect.DeepEqual(receipt.Accepted, old) {
			return ErrInvalid
		}
		expected = next
	}
	if !reflect.DeepEqual(expected, r) {
		return ErrInvalid
	}
	return nil
}
func Append(ctx context.Context, r Rehearsal, c AppendCommand, at time.Time) (Rehearsal, AppendReceipt, error) {
	if err := r.Validate(ctx); err != nil {
		return Rehearsal{}, AppendReceipt{}, err
	}
	return appendChecked(ctx, r, c, at)
}

func (r AppendReceipt) Validate(account string, c AppendCommand) error {
	if !paperaccount.ID.MatchString(account) || c.Validate() != nil || r.AccountID != account || r.AccountSHA256 != c.AccountSHA256 || !sameCommand(r.Accepted.Command, c) || !validTime(r.Accepted.AcceptedAt) || r.Accepted.AcceptedAt.Before(c.Entry.OccurredAt) || r.Accepted.Receipt.Sequence < 1 || r.Accepted.Receipt.Sequence > MaxEntries || r.Accepted.Receipt.EntryID != c.Entry.ID || r.Accepted.Receipt.Duplicate || !sha256Pattern.MatchString(r.Accepted.ResultSHA256) {
		return ErrInvalid
	}
	e := c.Entry
	e.OccurredAt = e.OccurredAt.UTC()
	expected := RetainedEntry{Sequence: r.Accepted.Receipt.Sequence, Entry: e, PreviousSHA256: c.ExpectedHeadSHA256}
	if digest(expected) != r.Accepted.Receipt.EntrySHA256 {
		return ErrInvalid
	}
	return nil
}
