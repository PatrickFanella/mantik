// Package fixturecapital defines the internal capital contract for the existing
// synthetic action worker. Storage must supply its locked action/effect view and
// atomically commit capital and action changes before activating this contract.
// There is no rehearsal, provider, model, JSON import or network mutation API.
package fixturecapital

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/actions"
	"git.subcult.tv/subculture-collective/mantik/internal/domain"
	"git.subcult.tv/subculture-collective/mantik/internal/library"
	"git.subcult.tv/subculture-collective/mantik/internal/paperaccount"
)

var ErrInvalid = errors.New("invalid synthetic fixture capital contract")
var ErrConflict = errors.New("fixture capital authority or reconciliation conflict")
var ErrLimit = errors.New("fixture capital limit exceeded")

const MaxCommitments = 256

// EffectReader must be the fixture effect store under the same transaction/lock
// as the source action. Nil means definitive local absence, never a timeout or
// missing provider response. Reader errors leave all capital held.
type EffectReader func(string) (*domain.ActionEffect, error)

type commitment struct {
	Intent domain.ActionIntent
	Amount int64
	Status string
	Effect *domain.ActionEffect
	Code   string
}
type Position struct {
	CaseID            string                  `json:"case_id"`
	StrategyVersionID string                  `json:"strategy_version_id"`
	EntryActionID     string                  `json:"entry_action_id"`
	Entry             domain.ExecutionOutcome `json:"entry"`
}

// Book has no JSON constructor or writable exported fields. Snapshots are
// detached readbacks, not capital commands or restorable spending authority.
type Book struct {
	caseID      string
	strategyID  string
	account     paperaccount.Account
	commitments map[string]commitment
	cash        int64
	reserved    int64
	position    *Position
	realized    int64
}
type Commitment struct {
	ActionID     string `json:"action_id"`
	DecisionID   string `json:"decision_id"`
	IntentSHA256 string `json:"intent_sha256"`
	AmountCents  int64  `json:"amount_cents"`
	Status       string `json:"status"`
	EffectSHA256 string `json:"effect_sha256,omitempty"`
	Code         string `json:"code,omitempty"`
}
type Snapshot struct {
	Schema              string       `json:"schema"`
	ReceiverID          string       `json:"receiver_id"`
	AccountID           string       `json:"account_id"`
	AccountSHA256       string       `json:"account_sha256"`
	OpeningCashCents    int64        `json:"opening_cash_cents"`
	CapitalLimitCents   int64        `json:"capital_limit_cents"`
	AvailableCashCents  int64        `json:"available_cash_cents"`
	ReservedCashCents   int64        `json:"reserved_cash_cents"`
	PositionCostCents   int64        `json:"position_cost_cents"`
	RealizedPnLCents    int64        `json:"realized_pnl_cents"`
	Position            *Position    `json:"position,omitempty"`
	Commitments         []Commitment `json:"commitments"`
	Authority           string       `json:"authority"`
	ServiceIntegrated   bool         `json:"service_integrated"`
	QualificationCredit bool         `json:"qualification_credit"`
	SHA256              string       `json:"sha256"`
}

func detach[T any](v T) T {
	raw, _ := json.Marshal(v)
	var copy T
	_ = json.Unmarshal(raw, &copy)
	return copy
}
func digest(v any) string { raw, _ := json.Marshal(v); return fmt.Sprintf("%x", sha256.Sum256(raw)) }
func New(a paperaccount.Account) (Book, error) {
	if a.Validate() != nil {
		return Book{}, ErrInvalid
	}
	return Book{account: a, cash: a.Config.OpeningCashCents, commitments: map[string]commitment{}}, nil
}
func (b Book) copy() Book {
	n := b
	n.commitments = make(map[string]commitment, len(b.commitments))
	for id, c := range b.commitments {
		n.commitments[id] = detach(c)
	}
	if b.position != nil {
		p := *b.position
		n.position = &p
	}
	return n
}
func (b Book) cost() int64 {
	if b.position == nil {
		return 0
	}
	return b.position.Entry.PriceCents * b.position.Entry.Quantity
}
func (b Book) valid() bool {
	if b.account.Validate() != nil || b.commitments == nil || len(b.commitments) > MaxCommitments || b.cash < 0 || b.reserved < 0 || b.cost() < 0 || b.reserved+b.cost() > b.account.Config.CapitalLimitCents || b.cash+b.reserved+b.cost() != b.account.Config.OpeningCashCents+b.realized {
		return false
	}
	if len(b.commitments) == 0 && (b.caseID != "" || b.strategyID != "") || len(b.commitments) > 0 && (b.caseID == "" || b.strategyID == "") {
		return false
	}
	var reserved int64
	for id, c := range b.commitments {
		if id != c.Intent.ID || c.Amount < 0 || c.Intent.Decision.CaseID != b.caseID || c.Intent.Decision.StrategyVersionID != b.strategyID {
			return false
		}
		if c.Status == "reserved" || c.Status == "held" {
			reserved += c.Amount
		}
	}
	return reserved == b.reserved
}
func (b Book) Snapshot() (Snapshot, error) {
	if !b.valid() {
		return Snapshot{}, ErrInvalid
	}
	s := Snapshot{Schema: "mantik-fixture-capital-contract-v1", ReceiverID: b.account.ReceiverID, AccountID: b.account.Config.ID, AccountSHA256: b.account.SHA256, OpeningCashCents: b.account.Config.OpeningCashCents, CapitalLimitCents: b.account.Config.CapitalLimitCents, AvailableCashCents: b.cash, ReservedCashCents: b.reserved, PositionCostCents: b.cost(), RealizedPnLCents: b.realized, Commitments: []Commitment{}, Authority: "synthetic_fixture_capital_contract_only"}
	if b.position != nil {
		p := *b.position
		s.Position = &p
	}
	for _, c := range b.commitments {
		v := Commitment{ActionID: c.Intent.ID, DecisionID: c.Intent.Decision.ID, IntentSHA256: digest(c.Intent), AmountCents: c.Amount, Status: c.Status, Code: c.Code}
		if c.Effect != nil {
			v.EffectSHA256 = digest(*c.Effect)
		}
		s.Commitments = append(s.Commitments, v)
	}
	slices.SortFunc(s.Commitments, func(a, b Commitment) int { return strings.Compare(a.ActionID, b.ActionID) })
	s.SHA256 = digest(s)
	return s, nil
}

// Only action/case fields used by Enqueue and Settle are copied. Unrelated
// routing inputs and histories are not needed to validate this economic effect.
func validationState(s domain.State, a domain.Action) domain.State {
	return domain.State{Strategy: s.Strategy, Case: detach(s.Case), LastOccurredAt: s.LastOccurredAt, Actions: &domain.ActionState{LastClock: s.Actions.LastClock, Items: map[string]domain.Action{a.Intent.ID: detach(a)}}, Routing: &domain.RoutingState{}}
}
func current(s domain.State, c domain.ActionClaim, now time.Time) (domain.Action, error) {
	a, err := actions.CheckClaim(s, c, now)
	if err != nil {
		return domain.Action{}, err
	}
	check := validationState(s, a)
	id, enqueueErr := actions.Enqueue(&check, a.Intent.Decision, a.Intent.Quote, a.Intent.Mode, a.Intent.TimerID)
	entry, err := library.ResolveStrategy(s.Strategy)
	if enqueueErr != nil || err != nil || c.ID != a.Intent.ID || id != a.Intent.ID || entry.EvidenceClass != "synthetic_unqualified" || a.Intent.Quote.Source != "fixture" || a.Intent.Quote.Symbol != "DEMO" || s.Case.ID == "" || s.Case.StrategyVersionID != s.Strategy.ID {
		return domain.Action{}, ErrInvalid
	}
	return a, nil
}

// Reserve is called before write-ahead submission. Exits retain a zero-cash
// commitment and must match this book's funded position; lack of free cash must
// not prevent a protective exit. Existing commitments cannot reserve twice.
func (b Book) Reserve(s domain.State, c domain.ActionClaim, now time.Time) (Book, error) {
	if !b.valid() || now.Before(b.account.CreatedAt) {
		return Book{}, ErrInvalid
	}
	a, err := current(s, c, now)
	if err != nil {
		return Book{}, err
	}
	if b.caseID != "" && (b.caseID != s.Case.ID || b.strategyID != s.Strategy.ID) {
		return Book{}, ErrConflict
	}
	if old, ok := b.commitments[a.Intent.ID]; ok {
		if !actions.Equal(old.Intent, a.Intent) || (old.Status != "reserved" && old.Status != "held") {
			return Book{}, ErrConflict
		}
		return b.copy(), nil
	}
	if a.Status != "pending" || actions.Terminal(a.Status) || len(b.commitments) >= MaxCommitments {
		return Book{}, ErrConflict
	}
	if _, _, err := actions.Evaluate(s, a, now); err != nil {
		return Book{}, err
	}
	for _, old := range b.commitments {
		if old.Status == "reserved" || old.Status == "held" {
			return Book{}, ErrConflict
		}
	}
	amount := int64(0)
	if a.Intent.Decision.Action == "enter" {
		amount = a.Intent.Quote.PriceCents * a.Intent.Decision.Quantity
		if b.position != nil || amount <= 0 || amount > b.cash || amount > b.account.Config.CapitalLimitCents-b.reserved-b.cost() {
			return Book{}, ErrLimit
		}
	} else {
		if b.position == nil || b.position.CaseID != s.Case.ID || b.position.StrategyVersionID != s.Strategy.ID || !actions.Equal(s.Case.Entry, b.position.Entry) {
			return Book{}, ErrConflict
		}
	}
	n := b.copy()
	n.caseID, n.strategyID = s.Case.ID, s.Strategy.ID
	n.commitments[a.Intent.ID] = commitment{Intent: detach(a.Intent), Amount: amount, Status: "reserved"}
	n.cash -= amount
	n.reserved += amount
	if !n.valid() {
		return Book{}, ErrInvalid
	}
	return n, nil
}

// Contains checks an existing commitment without creating one. Changed content
// under the same action identity is a conflict, including on terminal retries.
func (b Book) Contains(i domain.ActionIntent) (bool, error) {
	if !b.valid() {
		return false, ErrInvalid
	}
	old, ok := b.commitments[i.ID]
	if ok && !actions.Equal(old.Intent, i) {
		return false, ErrConflict
	}
	return ok, nil
}

// CheckSubmission requires a live funded commitment before the independent
// fixture effect transaction. It never recreates a missing reservation.
func (b Book) CheckSubmission(s domain.State, c domain.ActionClaim, now time.Time) error {
	a, err := current(s, c, now)
	if err != nil {
		return err
	}
	ok, err := b.Contains(a.Intent)
	if err != nil {
		return err
	}
	old := b.commitments[a.Intent.ID]
	if !ok || a.Status != "unknown" || (old.Status != "reserved" && old.Status != "held") || now.Before(b.account.CreatedAt) {
		return ErrConflict
	}
	return nil
}

// Reconcile consumes the owning store's effect lookup. A known matching result
// is checked by the existing action settlement contract before changing money.
// Missing effects release capital only when definitive local absence and current
// authority rejection/expiry agree; uncertainty and reader errors keep it held.
func (b Book) Reconcile(s domain.State, c domain.ActionClaim, now time.Time, read EffectReader) (Book, error) {
	if !b.valid() || read == nil || now.Before(b.account.CreatedAt) {
		return Book{}, ErrInvalid
	}
	a, err := current(s, c, now)
	if err != nil {
		return Book{}, err
	}
	old, ok := b.commitments[a.Intent.ID]
	if !ok || !actions.Equal(old.Intent, a.Intent) {
		return Book{}, ErrConflict
	}
	e, err := read(a.Intent.IdempotencyKey)
	if err != nil {
		return Book{}, err
	}
	if old.Status == "completed" {
		if actions.Terminal(a.Status) && a.Status != "completed" {
			return Book{}, ErrConflict
		}
		if e == nil || old.Effect == nil || !actions.Equal(*old.Effect, *e) {
			return Book{}, ErrConflict
		}
		if a.Status == "completed" && !actions.Equal(a.Outcome, old.Effect.Outcome) {
			return Book{}, ErrConflict
		}
		return b.copy(), nil
	}
	if old.Status == "released" {
		if a.Status == "completed" {
			return Book{}, ErrConflict
		}
		if e != nil {
			if old.Effect == nil || !actions.Equal(*old.Effect, *e) {
				return Book{}, ErrConflict
			}
		} else if old.Effect != nil {
			return Book{}, ErrConflict
		}
		return b.copy(), nil
	}
	if actions.Terminal(a.Status) {
		return Book{}, ErrConflict
	}
	n := b.copy()
	record := n.commitments[a.Intent.ID]
	if e == nil {
		if a.Status == "accepted" || a.Status == "partial" || old.Effect != nil {
			return Book{}, ErrConflict
		}
		if _, status, evaluateErr := actions.Evaluate(s, a, now); evaluateErr != nil {
			if status != "expired" && status != "rejected" {
				return Book{}, ErrConflict
			}
			n.cash += record.Amount
			n.reserved -= record.Amount
			record.Status = "released"
			record.Code = "definitive_fixture_absence_" + status
		} else if a.Status == "unknown" {
			record.Status = "held"
		}
	} else {
		if !actions.Equal(a.Intent, e.Intent) {
			return Book{}, ErrConflict
		}
		// Validate against a detached pre-ack state: settlement must not mutate the
		// caller's action/case while capital is only being calculated.
		check := validationState(s, a)
		if _, err := actions.Settle(&check, c, *e, now); err != nil {
			return Book{}, err
		}
		switch e.Status {
		case "accepted", "partial":
			record.Status = "held"
		case "rejected", "expired":
			if e.Outcome != nil {
				return Book{}, ErrConflict
			}
			n.cash += record.Amount
			n.reserved -= record.Amount
			record.Status = "released"
		case "completed":
			if e.Outcome == nil {
				return Book{}, ErrConflict
			}
			if a.Intent.Decision.Action == "enter" {
				if n.position != nil || e.Outcome.PriceCents*e.Outcome.Quantity != record.Amount {
					return Book{}, ErrConflict
				}
				n.reserved -= record.Amount
				n.position = &Position{CaseID: a.Intent.Decision.CaseID, StrategyVersionID: a.Intent.Decision.StrategyVersionID, EntryActionID: a.Intent.ID, Entry: *e.Outcome}
			} else {
				if n.position == nil || n.position.CaseID != s.Case.ID || !actions.Equal(s.Case.Entry, n.position.Entry) || e.Outcome.Quantity != n.position.Entry.Quantity {
					return Book{}, ErrConflict
				}
				proceeds := e.Outcome.PriceCents * e.Outcome.Quantity
				if proceeds <= 0 || proceeds > paperaccount.MaxOpeningCashCents || e.Outcome.RealizedPnLCents != proceeds-n.cost() {
					return Book{}, ErrConflict
				}
				n.cash += proceeds
				n.realized += e.Outcome.RealizedPnLCents
				n.position = nil
			}
			record.Status = "completed"
		default:
			return Book{}, ErrConflict
		}
		effect := detach(*e)
		record.Effect = &effect
	}
	n.commitments[a.Intent.ID] = record
	if !n.valid() {
		return Book{}, ErrInvalid
	}
	return n, nil
}
