// Package paperledger audits recorded synthetic accounting transitions offline.
// It does not authorize proposals, attest effects, or mutate service accounts.
package paperledger

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/paperaccount"
	"git.subcult.tv/subculture-collective/mantik/internal/workerplan"
)

const RecordingSchema = "mantik-synthetic-paper-ledger-recording-v1"
const ResultSchema = "mantik-synthetic-paper-ledger-audit-v1"
const MaxEntries = 256
const MaxReservations = 64
const MaxRecordingBytes = 1 << 20

var ErrInvalid = errors.New("invalid synthetic paper ledger recording")
var ErrConflict = errors.New("synthetic paper ledger transition conflicts with retained intent or state")
var ErrLimit = errors.New("synthetic paper ledger cash, capital or capacity limit exceeded")

type Entry struct {
	ID            string    `json:"id"`
	Kind          string    `json:"kind"`
	ReservationID string    `json:"reservation_id"`
	AmountCents   int64     `json:"amount_cents"`
	OccurredAt    time.Time `json:"occurred_at"`
}
type Recording struct {
	Schema        string               `json:"schema"`
	EvidenceClass string               `json:"evidence_class"`
	Account       paperaccount.Account `json:"account"`
	Entries       []Entry              `json:"entries"`
}
type Reservation struct {
	ID             string `json:"id"`
	OriginalCents  int64  `json:"original_cents"`
	RemainingCents int64  `json:"remaining_cents"`
	SettledCents   int64  `json:"settled_cents"`
	ReleasedCents  int64  `json:"released_cents"`
	Unresolved     bool   `json:"unresolved"`
}
type RetainedEntry struct {
	Sequence       int    `json:"sequence"`
	Entry          Entry  `json:"entry"`
	PreviousSHA256 string `json:"previous_sha256"`
	SHA256         string `json:"sha256"`
}
type Receipt struct {
	EntryID     string `json:"entry_id"`
	Sequence    int    `json:"sequence"`
	EntrySHA256 string `json:"entry_sha256"`
	Duplicate   bool   `json:"duplicate"`
}
type Result struct {
	Schema                 string          `json:"schema"`
	EvidenceClass          string          `json:"evidence_class"`
	ReceiverID             string          `json:"receiver_id"`
	AccountID              string          `json:"account_id"`
	AccountSHA256          string          `json:"account_sha256"`
	RecordingSHA256        string          `json:"recording_sha256"`
	Currency               string          `json:"currency"`
	OpeningCashCents       int64           `json:"opening_cash_cents"`
	CapitalLimitCents      int64           `json:"capital_limit_cents"`
	AvailableCashCents     int64           `json:"available_cash_cents"`
	ReservedCashCents      int64           `json:"reserved_cash_cents"`
	SettledDebitsCents     int64           `json:"settled_debits_cents"`
	UnresolvedReservations int             `json:"unresolved_reservations"`
	Reservations           []Reservation   `json:"reservations"`
	Entries                []RetainedEntry `json:"entries"`
	Receipts               []Receipt       `json:"receipts"`
	LastOccurredAt         time.Time       `json:"last_occurred_at"`
	LedgerHeadSHA256       string          `json:"ledger_head_sha256"`
	Authority              string          `json:"authority"`
	AllocationAvailable    bool            `json:"allocation_available"`
	ExecutionAttested      bool            `json:"execution_attested"`
	QualificationCredit    bool            `json:"qualification_credit"`
	SHA256                 string          `json:"sha256"`
}

func digest(v any) string {
	raw, _ := json.Marshal(v)
	h := sha256.Sum256(raw)
	return hex.EncodeToString(h[:])
}
func validTime(at time.Time) bool {
	return !at.IsZero() && at.UTC().Year() > 0 && at.UTC().Year() <= 9999
}
func Read(in io.Reader) (Recording, error) {
	raw, err := io.ReadAll(io.LimitReader(in, MaxRecordingBytes+1))
	if err != nil {
		return Recording{}, ErrInvalid
	}
	var r Recording
	if workerplan.ReadBoundedShape(raw, &r, MaxRecordingBytes) != nil {
		return Recording{}, ErrInvalid
	}
	return r, nil
}
func validEntry(e Entry) bool {
	if !paperaccount.ID.MatchString(e.ID) || !paperaccount.ID.MatchString(e.ReservationID) || !validTime(e.OccurredAt) {
		return false
	}
	switch e.Kind {
	case "reserve", "settle", "release":
		return e.AmountCents > 0 && e.AmountCents <= paperaccount.MaxOpeningCashCents
	case "unknown", "resolve":
		return e.AmountCents == 0
	default:
		return false
	}
}

// Audit replays each unique command once. Exact earlier retries do not advance
// the ledger clock or change projections, even after subsequent transitions.
func Audit(ctx context.Context, input Recording) (Result, error) {
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}
	if input.Schema != RecordingSchema || input.EvidenceClass != "synthetic_unqualified" || input.Account.Validate() != nil || input.Entries == nil || len(input.Entries) > MaxEntries {
		return Result{}, ErrInvalid
	}
	for _, e := range input.Entries {
		if !validEntry(e) {
			return Result{}, ErrInvalid
		}
	}
	raw, err := json.Marshal(input)
	if err != nil || len(raw) > MaxRecordingBytes {
		return Result{}, ErrInvalid
	}
	r := input
	r.Entries = append([]Entry{}, input.Entries...)
	for i := range r.Entries {
		r.Entries[i].OccurredAt = r.Entries[i].OccurredAt.UTC()
	}
	out := Result{Schema: ResultSchema, EvidenceClass: r.EvidenceClass, ReceiverID: r.Account.ReceiverID, AccountID: r.Account.Config.ID, AccountSHA256: r.Account.SHA256, RecordingSHA256: digest(r), Currency: r.Account.Config.Currency, OpeningCashCents: r.Account.Config.OpeningCashCents, CapitalLimitCents: r.Account.Config.CapitalLimitCents, AvailableCashCents: r.Account.Config.OpeningCashCents, Reservations: []Reservation{}, Entries: []RetainedEntry{}, Receipts: []Receipt{}, LastOccurredAt: r.Account.CreatedAt, LedgerHeadSHA256: r.Account.SHA256, Authority: "recorded_synthetic_paper_ledger_only"}
	reservations := map[string]Reservation{}
	seen := map[string]RetainedEntry{}
	for index, e := range r.Entries {
		if err := ctx.Err(); err != nil {
			return Result{}, err
		}
		fail := func(err error) (Result, error) { return Result{}, fmt.Errorf("entry %d: %w", index+1, err) }
		if !validEntry(e) {
			return fail(ErrInvalid)
		}
		if old, ok := seen[e.ID]; ok {
			if old.Entry != e {
				return fail(ErrConflict)
			}
			out.Receipts = append(out.Receipts, Receipt{EntryID: e.ID, Sequence: old.Sequence, EntrySHA256: old.SHA256, Duplicate: true})
			continue
		}
		if e.OccurredAt.Before(out.LastOccurredAt) {
			return fail(ErrConflict)
		}
		current, exists := reservations[e.ReservationID]
		switch e.Kind {
		case "reserve":
			if exists {
				return fail(ErrConflict)
			}
			if len(reservations) >= MaxReservations || e.AmountCents > out.AvailableCashCents || e.AmountCents > out.CapitalLimitCents-out.ReservedCashCents-out.SettledDebitsCents {
				return fail(ErrLimit)
			}
			current = Reservation{ID: e.ReservationID, OriginalCents: e.AmountCents, RemainingCents: e.AmountCents}
			out.AvailableCashCents -= e.AmountCents
			out.ReservedCashCents += e.AmountCents
		case "unknown":
			if !exists || current.RemainingCents == 0 || current.Unresolved {
				return fail(ErrConflict)
			}
			current.Unresolved = true
		case "resolve":
			if !exists || !current.Unresolved {
				return fail(ErrConflict)
			}
			current.Unresolved = false
		case "settle", "release":
			if !exists || current.Unresolved || e.AmountCents > current.RemainingCents {
				return fail(ErrConflict)
			}
			current.RemainingCents -= e.AmountCents
			out.ReservedCashCents -= e.AmountCents
			if e.Kind == "settle" {
				current.SettledCents += e.AmountCents
				out.SettledDebitsCents += e.AmountCents
			} else {
				current.ReleasedCents += e.AmountCents
				out.AvailableCashCents += e.AmountCents
			}
		}
		reservations[e.ReservationID] = current
		// Integer arithmetic stays bounded by the validated opening cash. A settled
		// debit remains committed; no sale/proceeds or implicit expiry credit exists.
		if out.AvailableCashCents < 0 || out.ReservedCashCents < 0 || out.SettledDebitsCents < 0 || out.AvailableCashCents+out.ReservedCashCents+out.SettledDebitsCents != out.OpeningCashCents || out.ReservedCashCents+out.SettledDebitsCents > out.CapitalLimitCents {
			return fail(ErrInvalid)
		}
		retained := RetainedEntry{Sequence: len(out.Entries) + 1, Entry: e, PreviousSHA256: out.LedgerHeadSHA256}
		retained.SHA256 = digest(retained)
		out.Entries = append(out.Entries, retained)
		seen[e.ID] = retained
		out.LedgerHeadSHA256 = retained.SHA256
		out.LastOccurredAt = e.OccurredAt
		out.Receipts = append(out.Receipts, Receipt{EntryID: e.ID, Sequence: retained.Sequence, EntrySHA256: retained.SHA256})
	}
	for _, v := range reservations {
		out.Reservations = append(out.Reservations, v)
		if v.Unresolved {
			out.UnresolvedReservations++
		}
	}
	slices.SortFunc(out.Reservations, func(a, b Reservation) int { return strings.Compare(a.ID, b.ID) })
	out.SHA256 = digest(out)
	return out, nil
}
