package account

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"reflect"
	"slices"
	"strings"
	"time"
	"unicode"

	"git.subcult.tv/subculture-collective/mantik/internal/allocation"
	"git.subcult.tv/subculture-collective/mantik/internal/risk"
)

// OwnerBookAction is a confirmed, version-pinned owner request. It is evidence
// of intent, not authentication: the service must authenticate its caller.
// Source names the owner's real cash receipt, never an assumed scheduled flow.
type OwnerBookAction struct {
	ExpectedVersion                    int64
	Action, StrategyID, Amount, Source string
	EffectiveAt                        time.Time
	Confirmation                       string
}

// ConfirmationPin binds the exact terms, book, action ID and journal version.
func (a OwnerBookAction) ConfirmationPin(book, id string) string {
	a.Confirmation = ""
	raw, err := json.Marshal(struct {
		Book, ID string
		Terms    OwnerBookAction
	}{book, id, a})
	if err != nil {
		return ""
	}
	return fmt.Sprintf("confirm-%x", sha256.Sum256(raw))
}

// Validate checks supported terms and explicit confirmation without granting authority.
func (a OwnerBookAction) Validate(book, id string) error {
	if a.ConfirmationPin(book, id) == "" || !bookID.MatchString(book) || !bookID.MatchString(id) || a.ExpectedVersion < 0 || a.ExpectedVersion == math.MaxInt64 || a.Confirmation != a.ConfirmationPin(book, id) {
		return errors.New("invalid owner action identity, version or confirmation")
	}
	switch a.Action {
	case "pause_strategy", "retire_strategy", "resume_strategy":
		if len(a.StrategyID) == 0 || len(a.StrategyID) > 128 || strings.IndexFunc(a.StrategyID, unicode.IsControl) >= 0 {
			return errors.New("invalid owner strategy")
		}
	case "owner_clear_halt":
		if a.StrategyID != "" {
			return errors.New("halt clear is a book control")
		}
	case "deposit":
		if a.StrategyID != "" || a.Amount != "500" || a.EffectiveAt.IsZero() || len(a.Source) == 0 || len(a.Source) > 256 || strings.TrimSpace(a.Source) != a.Source || strings.IndexFunc(a.Source, unicode.IsControl) >= 0 {
			return errors.New("real deposit requires amount 500, actual economic clock and receipt source")
		}
	default:
		return errors.New("unsupported owner action")
	}
	if a.Action != "deposit" && (a.Amount != "" || a.Source != "" || !a.EffectiveAt.IsZero()) {
		return errors.New("unexpected deposit terms")
	}
	return nil
}

func validateOwnerEvent(cfg CappedConfig, c BookCommand, version int64) error {
	a := c.OwnerAction
	if err := a.Validate(cfg.ID, c.ID); err != nil {
		return err
	}
	if a.ExpectedVersion != version {
		return ErrBookConflict
	}
	if a.Action == "deposit" {
		if cfg.FundingMode != OwnerReceipts || cfg.OpeningCash != "2000" || cfg.DepositAmount != "500" || cfg.EveryWeeks != 2 || c.Kind != "deposit" || c.Amount != a.Amount || !c.EffectiveAt.Equal(a.EffectiveAt) || c.RiskControl != nil {
			return errors.New("real deposit differs from fixed book terms")
		}
	} else if c.Kind != "risk_control" || c.RiskControl == nil || *c.RiskControl != (risk.Control{Action: a.Action, StrategyID: a.StrategyID}) {
		return errors.New("owner control differs from confirmation")
	}
	// Owner terms cannot attest a different event or hide executable sidecars.
	// Clear alone may refresh complete valuation marks before resetting baseline.
	expected := BookCommand{ID: c.ID, At: c.At, Kind: c.Kind, OwnerAction: a}
	if a.Action == "deposit" {
		expected.Amount, expected.EffectiveAt = a.Amount, a.EffectiveAt
	} else {
		expected.RiskControl = &risk.Control{Action: a.Action, StrategyID: a.StrategyID}
		if a.Action == "owner_clear_halt" {
			expected.Marks = c.Marks
		}
	}
	if !reflect.DeepEqual(expected, c) {
		return errors.New("owner event differs from confirmed payload")
	}
	return nil
}

// OwnerSleeveView combines immutable ownership with current controls and the
// latest finalized review/distribution. Historical budgets are labelled as such.
type OwnerSleeveView struct {
	Sleeve          allocation.Sleeve
	Paused, Retired bool
	LastAllocation  *allocation.SleeveAllocation
	LastReview      *StrategyReview
}

// OwnerBookView is an on-demand local-paper readback. Missing current valuation
// evidence never turns into zero equity. It does not assert broker health or TWR.
type OwnerBookView struct {
	Config                                       CappedConfig
	Version                                      int64
	AsOf                                         time.Time
	Cash, SettledCash, Equity, EquityUnavailable string
	Positions                                    map[string]string
	Halted                                       bool
	Risk                                         risk.State
	Sleeves                                      []OwnerSleeveView
	Deposits                                     []BookCommand
}

// ReadOwnerBook reports controls even when marks needed for equity are unavailable.
func ReadOwnerBook(s CappedState, at time.Time) (OwnerBookView, error) {
	r, err := replay(s)
	if err != nil {
		return OwnerBookView{}, err
	}
	if at.Before(s.Config.OpenedAt) || len(s.Events) > 0 && at.Before(s.Events[len(s.Events)-1].At) {
		return OwnerBookView{}, errors.New("readback precedes book history")
	}
	r.funds.Advance(civil(at))
	out := OwnerBookView{Config: s.Config, Version: s.Version, AsOf: at, Cash: r.funds.Cash().FloatString(9), SettledCash: r.funds.Settled().FloatString(9), Positions: map[string]string{}, Risk: r.risk, Halted: r.halted || r.risk.Halted, Sleeves: []OwnerSleeveView{}, Deposits: []BookCommand{}}
	for symbol, q := range r.positions {
		out.Positions[symbol] = q.FloatString(9)
	}
	prices, err := CurrentPortfolioMarks(s, at)
	if err == nil {
		var ps []Position
		ps, err = marks(r.positions, prices)
		if err == nil {
			out.Equity = r.funds.Status(ps).Equity.FloatString(9)
		}
	}
	if err != nil {
		out.EquityUnavailable = err.Error()
	}
	keys := []string{}
	for id := range r.sleeves {
		keys = append(keys, id)
	}
	slices.Sort(keys)
	for _, id := range keys {
		sleeve := r.sleeves[id]
		v := OwnerSleeveView{Sleeve: sleeve, Paused: r.risk.Breakers[sleeve.StrategyID], Retired: r.risk.Retired[sleeve.StrategyID] || r.reviews[id].Retired}
		if r.distribution != nil {
			for _, a := range r.distribution.Sleeves {
				if a.SleeveID == id {
					copy := a
					v.LastAllocation = &copy
				}
			}
		}
		for _, event := range s.Events {
			if event.Review != nil && event.Review.SleeveID == id {
				copy := *event.Review
				v.LastReview = &copy
			}
		}
		out.Sleeves = append(out.Sleeves, v)
	}
	for _, event := range s.Events {
		if event.Kind == "deposit" {
			out.Deposits = append(out.Deposits, event)
		}
	}
	return out, nil
}
