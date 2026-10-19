package fixturecapital

import (
	"regexp"
	"strings"

	"git.subcult.tv/subculture-collective/mantik/internal/paperaccount"
)

var hashPattern = regexp.MustCompile(`^[a-f0-9]{64}$`)

// Validate checks a detached readback against immutable funding. It cannot turn
// that readback into a book, a reservation or execution authority.
func (s Snapshot) Validate(a paperaccount.Account) error {
	max := paperaccount.MaxOpeningCashCents
	if a.Validate() != nil || s.Schema != "mantik-fixture-capital-contract-v1" || s.Authority != "synthetic_fixture_capital_contract_only" || s.ServiceIntegrated || s.QualificationCredit || s.ReceiverID != a.ReceiverID || s.AccountID != a.Config.ID || s.AccountSHA256 != a.SHA256 || s.OpeningCashCents != a.Config.OpeningCashCents || s.CapitalLimitCents != a.Config.CapitalLimitCents || s.AvailableCashCents < 0 || s.AvailableCashCents > 2*max || s.ReservedCashCents < 0 || s.ReservedCashCents > max || s.PositionCostCents < 0 || s.PositionCostCents > max || s.RealizedPnLCents < -max || s.RealizedPnLCents > max || s.AvailableCashCents+s.ReservedCashCents+s.PositionCostCents != s.OpeningCashCents+s.RealizedPnLCents || s.ReservedCashCents+s.PositionCostCents > s.CapitalLimitCents || s.Commitments == nil || len(s.Commitments) > MaxCommitments {
		return ErrInvalid
	}
	var held int64
	entryFound := false
	for i, c := range s.Commitments {
		if !strings.HasPrefix(c.ActionID, "action-") || !hashPattern.MatchString(strings.TrimPrefix(c.ActionID, "action-")) || c.DecisionID == "" || len(c.DecisionID) > 256 || !hashPattern.MatchString(c.IntentSHA256) || c.AmountCents < 0 || c.AmountCents > s.CapitalLimitCents || len(c.Code) > 128 || i > 0 && s.Commitments[i-1].ActionID >= c.ActionID || c.EffectSHA256 != "" && !hashPattern.MatchString(c.EffectSHA256) {
			return ErrInvalid
		}
		switch c.Status {
		case "reserved", "held":
			held += c.AmountCents
		case "completed":
			if c.EffectSHA256 == "" {
				return ErrInvalid
			}
		case "released":
		default:
			return ErrInvalid
		}
		if s.Position != nil && c.ActionID == s.Position.EntryActionID && c.DecisionID == s.Position.Entry.DecisionID && c.Status == "completed" && c.AmountCents == s.PositionCostCents {
			entryFound = true
		}
	}
	if held != s.ReservedCashCents {
		return ErrInvalid
	}
	if s.Position == nil {
		if s.PositionCostCents != 0 {
			return ErrInvalid
		}
	} else {
		p := s.Position
		e := p.Entry
		if !entryFound || p.CaseID == "" || p.StrategyVersionID == "" || len(p.CaseID) > 256 || len(p.StrategyVersionID) > 256 || e.ID != p.EntryActionID+"-execution" || e.Action != "enter" || e.Mode != "paper_fixture" || e.FillModel != "observed_quote_zero_cost_v1" || e.PriceCents < 1 || e.PriceCents > 100000000 || e.Quantity < 1 || e.Quantity > 10000 || e.PriceCents*e.Quantity != s.PositionCostCents || e.RealizedPnLCents != 0 || e.ExecutedAt.IsZero() {
			return ErrInvalid
		}
	}
	want := s.SHA256
	s.SHA256 = ""
	if !hashPattern.MatchString(want) || digest(s) != want {
		return ErrInvalid
	}
	return nil
}
