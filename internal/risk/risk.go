// Package risk enforces the portfolio's fixed limits independently of strategies
// and models. Its state is retained by the capped-book command journal.
package risk

import (
	"errors"
	"math/big"
	"regexp"
	"time"
)

var amountText = regexp.MustCompile(`^(0|[1-9][0-9]{0,17})(\.[0-9]{1,18})?$`)

func signedAmount(s string) (*big.Rat, error) {
	if len(s) > 0 && s[0] == '-' {
		r, e := amount(s[1:])
		if e != nil {
			return nil, e
		}
		return r.Neg(r), nil
	}
	return amount(s)
}

func amount(s string) (*big.Rat, error) {
	if !amountText.MatchString(s) {
		return nil, errors.New("invalid risk amount")
	}
	r, _ := new(big.Rat).SetString(s)
	return r, nil
}

// State is one view of the persistent brake. Only explicit owner controls clear
// a hard halt; an improving mark, deposit, restart or new day never clears it.
type State struct {
	At                                          time.Time
	Equity, Deposits, NAV, HighNAV, DayNAV, Day string
	Warn, Paused, Halted, Kill, ReduceOnly      bool
	PauseReason                                 string
	Breakers                                    map[string]bool
}

// Observe advances a unitized marked return, removing recorded external flows.
// The journal supplies equity and cumulative deposits, never a broker balance.
func Observe(s State, at time.Time, equity, deposits string) (State, error) {
	e, err := signedAmount(equity)
	flow, fe := amount(deposits)
	if err != nil || fe != nil || at.IsZero() || (!s.At.IsZero() && at.Before(s.At)) {
		return s, errors.New("invalid risk observation")
	}
	z, err := time.LoadLocation("America/New_York")
	if err != nil {
		return s, err
	}
	day := at.In(z).Format(time.DateOnly)
	if s.At.IsZero() {
		s.NAV, s.HighNAV, s.DayNAV, s.Day = "1", "1", "1", day
		if e.Sign() <= 0 {
			s.NAV = "0"
			s.Halted = true
		}
	} else {
		prev, pe := signedAmount(s.Equity)
		oldFlow, oe := amount(s.Deposits)
		nav, ne := amount(s.NAV)
		if pe != nil || oe != nil || ne != nil || flow.Cmp(oldFlow) < 0 {
			return s, errors.New("invalid retained risk observation")
		}
		if day != s.Day {
			s.Day, s.DayNAV = day, s.NAV
		}
		change := new(big.Rat).Sub(flow, oldFlow)
		value := new(big.Rat).Sub(e, change)
		if value.Sign() < 0 {
			value.SetInt64(0)
		}
		if prev.Sign() > 0 {
			nav.Mul(nav, new(big.Rat).Quo(value, prev))
		} else {
			nav.SetInt64(0)
			s.Halted = true
		}
		s.NAV = nav.FloatString(18)
		high, he := amount(s.HighNAV)
		if he != nil {
			return s, he
		}
		if nav.Cmp(high) > 0 {
			s.HighNAV = s.NAV
		}
	}
	s.At, s.Equity, s.Deposits = at.UTC(), e.FloatString(18), flow.FloatString(18)
	nav, _ := amount(s.NAV)
	high, _ := amount(s.HighNAV)
	start, _ := amount(s.DayNAV)
	if high.Sign() <= 0 || start.Sign() <= 0 {
		s.Halted = true
		return s, nil
	}
	drawdown := new(big.Rat).Sub(big.NewRat(1, 1), new(big.Rat).Quo(nav, high))
	daily := new(big.Rat).Sub(big.NewRat(1, 1), new(big.Rat).Quo(nav, start))
	s.Warn = drawdown.Cmp(big.NewRat(5, 100)) >= 0
	if daily.Cmp(big.NewRat(3, 100)) >= 0 {
		s.Paused, s.PauseReason = true, "risk_daily_loss"
	}
	if drawdown.Cmp(big.NewRat(10, 100)) >= 0 {
		s.Paused, s.PauseReason = true, "risk_drawdown_pause"
	}
	if drawdown.Cmp(big.NewRat(15, 100)) >= 0 || e.Sign() <= 0 {
		s.Halted = true
	}
	return s, nil
}

// Control is an operator command, separate from a strategy's target interface.
type Control struct{ Action, StrategyID string }

// ApplyControl applies explicit owner controls. Resume does not clear a halt.
// The caller authenticates the operator; a value is not an identity attestation.
func ApplyControl(s State, c Control) (State, error) {
	b := map[string]bool{}
	for id, v := range s.Breakers {
		b[id] = v
	}
	s.Breakers = b
	switch c.Action {
	case "kill":
		s.Kill = true
	case "unkill":
		s.Kill = false
	case "reduce_only":
		s.ReduceOnly = true
	case "resume":
		s.ReduceOnly, s.Paused, s.PauseReason = false, false, ""
	case "owner_clear_halt":
		e, equityErr := signedAmount(s.Equity)
		if equityErr != nil || e.Sign() <= 0 {
			return s, errors.New("cannot clear halt without a positive observed book")
		}
		s.Halted = false
		s.Warn = false
		s.Paused, s.PauseReason = false, ""
		// Clearing is an explicit owner loss-baseline reset. Ordinary resume,
		// deposits and a new day never perform this reset.
		s.NAV, s.HighNAV, s.DayNAV = "1", "1", "1"
	case "pause_strategy", "resume_strategy":
		if c.StrategyID == "" || len(c.StrategyID) > 128 {
			return s, errors.New("invalid strategy breaker")
		}
		s.Breakers[c.StrategyID] = c.Action == "pause_strategy"
	default:
		return s, errors.New("unknown risk control")
	}
	return s, nil
}

// OrderView contains projected conservative exposures after this order, with
// independent pending opening legs and all fees reserved. Reducing is true only
// for a quantity that cannot cross zero, even if other pending reductions fill.
type OrderView struct {
	Incubation, ModelVersion                   string
	StrategyID                                 string
	Equity, CashAfter, Gross, Position, Sleeve string
	Reducing                                   bool
}

// Check returns every failed hard check. Emergency controls permit reductions
// but never a position flip or a capacity-increasing leg.
func Check(s State, v OrderView) ([]string, error) {
	e, err := signedAmount(v.Equity)
	c, ce := signedAmount(v.CashAfter)
	g, ge := amount(v.Gross)
	p, pe := amount(v.Position)
	sl, se := amount(v.Sleeve)
	pool, model := v.Incubation, v.ModelVersion
	if pool == "" {
		pool = "0"
	}
	if model == "" {
		model = "0"
	}
	incubation, ie := amount(pool)
	version, ve := amount(model)
	if err != nil || ce != nil || ge != nil || pe != nil || se != nil || ie != nil || ve != nil || s.At.IsZero() {
		return nil, errors.New("invalid risk order view")
	}
	if v.Reducing {
		return []string{}, nil
	}
	if e.Sign() <= 0 {
		return []string{"risk_hard_halt"}, nil
	}
	reasons := []string{}
	if s.Halted {
		reasons = append(reasons, "risk_hard_halt")
	}
	if s.Kill {
		reasons = append(reasons, "risk_kill_switch")
	}
	if s.ReduceOnly {
		reasons = append(reasons, "risk_reduce_only")
	}
	if s.Paused {
		reasons = append(reasons, s.PauseReason)
	}
	if s.Breakers[v.StrategyID] {
		reasons = append(reasons, "risk_strategy_breaker")
	}
	if g.Cmp(e) > 0 {
		reasons = append(reasons, "risk_gross_limit")
	}
	if p.Cmp(new(big.Rat).Mul(e, big.NewRat(5, 100))) > 0 {
		reasons = append(reasons, "risk_position_limit")
	}
	if sl.Cmp(new(big.Rat).Mul(e, big.NewRat(20, 100))) > 0 {
		reasons = append(reasons, "risk_sleeve_limit")
	}
	if c.Cmp(new(big.Rat).Mul(e, big.NewRat(20, 100))) < 0 {
		reasons = append(reasons, "risk_cash_reserve")
	}
	if incubation.Cmp(new(big.Rat).Mul(e, big.NewRat(5, 100))) > 0 {
		reasons = append(reasons, "risk_incubation_pool")
	}
	if version.Cmp(new(big.Rat).Mul(e, big.NewRat(1, 100))) > 0 {
		reasons = append(reasons, "risk_model_version_limit")
	}
	return reasons, nil
}
