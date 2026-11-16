// Package forward declares prospective evidence windows for canonical strategies.
// These records grant neither qualification credit nor execution authority.
package forward

import (
	"context"
	"errors"
	"math/big"
	"regexp"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/strategyidentity"
	"git.subcult.tv/subculture-collective/mantik/internal/strategytrial"
)

const Schema = "mantik-forward-declaration-v1"
const MaxSeconds int64 = 90 * 24 * 60 * 60

var (
	decimal     = regexp.MustCompile(`^-?(0|[1-9][0-9]*)(\.[0-9]{1,18})?$`)
	ErrInvalid  = errors.New("invalid forward declaration")
	ErrConflict = errors.New("forward declaration conflicts with retained source")
	ErrNotFound = errors.New("forward declaration not found")
)

// Benchmark pins a separately versioned strategy, including its own parameters.
// A cash benchmark can be represented by a strategy returning empty targets.
type Benchmark struct {
	StrategyID string                `json:"strategy_id"`
	Spec       strategyidentity.Spec `json:"spec"`
}

// Declaration freezes evidence requirements, not caller-supplied results.
// EligibilityTTLSeconds starts at WindowStart; decision eligibility ends at the
// earlier of that expiry and WindowEnd. Holds may continue into the outcome tail.
type Declaration struct {
	ID                      string                `json:"id"`
	Family                  string                `json:"family"`
	GateID                  string                `json:"gate_id"`
	BookID                  string                `json:"book_id"`
	SleeveID                string                `json:"sleeve_id"`
	StrategyID              string                `json:"strategy_id"`
	Spec                    strategyidentity.Spec `json:"spec"`
	Benchmark               Benchmark             `json:"benchmark"`
	WindowStart             time.Time             `json:"window_start"`
	WindowEnd               time.Time             `json:"window_end"`
	OutcomeDeadline         time.Time             `json:"outcome_deadline"`
	MaxHoldingSeconds       int64                 `json:"max_holding_seconds"`
	EligibilityTTLSeconds   int64                 `json:"eligibility_ttl_seconds"`
	MinSessions             int                   `json:"min_sessions"`
	MinIndependentDecisions int                   `json:"min_independent_decisions"`
	MinimumNetExcess        string                `json:"minimum_net_excess"`
	MaximumDrawdown         string                `json:"maximum_drawdown"`
	MaxSearchTrials         int64                 `json:"max_search_trials"`
}

func validTime(at time.Time) bool { return !at.IsZero() && at.Year() >= 1900 && at.Year() <= 9998 }

// Validate checks the new contract without changing legacy threshold semantics.
func (d Declaration) Validate() error {
	sid, se := d.Spec.ID()
	bid, be := d.Benchmark.Spec.ID()
	excess, ex := new(big.Rat).SetString(d.MinimumNetExcess)
	drawdown, dd := new(big.Rat).SetString(d.MaximumDrawdown)
	if !strategytrial.ValidID(d.ID) || !strategytrial.ValidID(d.GateID) || !strategytrial.ValidID(d.BookID) || !strategytrial.ValidID(d.SleeveID) || len(d.SleeveID) > 64 || !strategytrial.ValidFamily(d.Family) || se != nil || be != nil || sid != d.StrategyID || bid != d.Benchmark.StrategyID || !validTime(d.WindowStart) || !validTime(d.WindowEnd) || !validTime(d.OutcomeDeadline) || !d.WindowEnd.After(d.WindowStart) || d.WindowEnd.Sub(d.WindowStart) > time.Duration(MaxSeconds)*time.Second || d.MaxHoldingSeconds < 1 || d.MaxHoldingSeconds > MaxSeconds || d.EligibilityTTLSeconds < 1 || d.EligibilityTTLSeconds > MaxSeconds || !d.OutcomeDeadline.After(d.WindowEnd) || d.OutcomeDeadline.Before(d.WindowEnd.Add(time.Duration(d.MaxHoldingSeconds)*time.Second)) || d.OutcomeDeadline.Sub(d.WindowEnd) > time.Duration(MaxSeconds)*time.Second || d.MinSessions < 2 || d.MinSessions > 1000000 || d.MinIndependentDecisions < 2 || d.MinIndependentDecisions > 1000000 || d.MaxSearchTrials < 1 || d.MaxSearchTrials > 1000000 || len(d.MinimumNetExcess) > 32 || len(d.MaximumDrawdown) > 32 || !decimal.MatchString(d.MinimumNetExcess) || !decimal.MatchString(d.MaximumDrawdown) || !ex || !dd || excess.Cmp(big.NewRat(-1, 1)) < 0 || excess.Cmp(big.NewRat(1, 1)) > 0 || drawdown.Sign() < 0 || drawdown.Cmp(big.NewRat(1, 1)) > 0 {
		return ErrInvalid
	}
	return nil
}

// Record retains the exact family starts and gate/book source binding. Outcomes
// are deliberately absent: failed and unfinished attempts count equally.
type Record struct {
	Schema              string                `json:"schema"`
	Declaration         Declaration           `json:"declaration"`
	DeclaredAt          time.Time             `json:"declared_at"`
	SearchStarts        []strategytrial.Start `json:"search_starts"`
	SearchTotal         int64                 `json:"search_total"`
	SourceSHA256        string                `json:"source_sha256"`
	QualificationCredit bool                  `json:"qualification_credit"`
	SHA256              string                `json:"sha256"`
}

// Eligibility describes current clocks and registry coverage. It is not a
// qualification assessment, admission, promotion, or a return calculation.
type Eligibility struct {
	DeclarationID       string    `json:"declaration_id"`
	At                  time.Time `json:"at"`
	SearchTotal         int64     `json:"search_total"`
	EntryEligible       bool      `json:"entry_eligible"`
	OutcomeOpen         bool      `json:"outcome_open"`
	HoldExpired         bool      `json:"hold_expired"`
	QualificationCredit bool      `json:"qualification_credit"`
}

// Clocks uses half-open entry/holding/outcome intervals. Equality at an expiry
// fails closed; equality at WindowStart permits a new decision.
func (r Record) Clocks(at time.Time, n int64, opened time.Time) Eligibility {
	d := r.Declaration
	end := d.WindowStart.Add(time.Duration(d.EligibilityTTLSeconds) * time.Second)
	if d.WindowEnd.Before(end) {
		end = d.WindowEnd
	}
	out := Eligibility{DeclarationID: d.ID, At: at.UTC(), SearchTotal: n}
	out.HoldExpired = !opened.IsZero() && !at.Before(opened.Add(time.Duration(d.MaxHoldingSeconds)*time.Second))
	out.OutcomeOpen = !at.Before(d.WindowStart) && at.Before(d.OutcomeDeadline) && n >= r.SearchTotal && n <= d.MaxSearchTrials
	out.EntryEligible = out.OutcomeOpen && at.Before(end) && !out.HoldExpired
	return out
}

// Store supplies retry-safe declarations and current, storage-derived eligibility.
type Store interface {
	DeclareForward(context.Context, Declaration, func() time.Time) (Record, error)
	ForwardDeclaration(context.Context, string) (Record, error)
	ForwardEligibility(context.Context, string, time.Time) (Eligibility, error)
}
