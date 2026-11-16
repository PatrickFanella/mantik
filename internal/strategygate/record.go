// Package strategygate defines immutable backtest declarations and their recorded
// decisions. A decision grants at most paper incubation, never qualification.
package strategygate

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"regexp"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/strategytrial"
)

var (
	ErrInvalid   = errors.New("invalid strategy gate record")
	ErrConflict  = errors.New("strategy gate record conflicts")
	ErrNotFound  = errors.New("strategy gate not found")
	strategyHash = regexp.MustCompile(`^strategy-[a-f0-9]{64}$`)
	digest       = regexp.MustCompile(`^[a-f0-9]{64}$`)
)

// Variant freezes one named strategy version before running the experiment.
type Variant struct {
	Name    string
	Spec    json.RawMessage
	Minimum json.RawMessage
}

// Reference is a retained, dated benchmark declaration.
type Reference struct {
	Name, DataSHA256 string
	Returns          []ReferenceReturn
}

// ReferenceReturn is one decimal daily benchmark return.
type ReferenceReturn struct{ Date, Return string }

// Declaration freezes the family, calendar, recipe neighbours, current capital
// and benchmark inputs before any attempt starts. Coverage and Availability are
// trusted source-review assertions, not attestations inferred from a hash.
type Declaration struct {
	SettlementCalendar                                                                       string
	EngineVersion, Account                                                                   string
	BaseCost, StressCost                                                                     json.RawMessage
	Unlevered                                                                                bool
	Assets                                                                                   []string
	ID, Family, ExperimentID, StrategyID, BaseCandidate, Balance, Policy, Benchmark, Minimum string
	Spec                                                                                     json.RawMessage
	Candidates                                                                               []Variant
	Calendar                                                                                 []string
	Train, Test, Purge, Embargo                                                              int
	Cash, SPY                                                                                Reference
	DataSHA256                                                                               string
	Coverage, Availability                                                                   bool
}

// Validate checks bounded declaration shape. The backtest evaluator also checks
// strategy identities and the exact registry experiments before deciding.
func (d Declaration) Validate() error {
	if !strategytrial.ValidID(d.ID) || !strategytrial.ValidFamily(d.Family) || !strategyHash.MatchString(d.StrategyID) || !json.Valid(d.Spec) || len(d.Spec) > 64<<10 || !digest.MatchString(d.DataSHA256) || len(d.Candidates) > 32 || len(d.Calendar) > 20000 || d.Policy != "backtest-gate-v1" || (d.Benchmark != "cash" && d.Benchmark != "spy") {
		return ErrInvalid
	}
	raw, e := json.Marshal(d)
	if e != nil || len(raw) > 2<<20 {
		return ErrInvalid
	}
	return nil
}

// Hash binds a retained record to the exact predeclared evidence and rules.
func Hash(v any) (string, error) {
	raw, e := json.Marshal(v)
	if e != nil {
		return "", e
	}
	h := sha256.Sum256(raw)
	return hex.EncodeToString(h[:]), nil
}

// Check retains each failed, passed or explicitly skipped check and its reason.
type Check struct {
	Code, Reason    string
	Passed, Skipped bool
}

// Metric preserves both values and explicit unavailability in a gate report.
type Metric struct {
	Value  *float64
	Reason string
}

// Diagnostic records the actual statistics behind a fold's checks.
type Diagnostic struct {
	TrialID                         string
	DSR, PBO, CashExcess, SPYExcess Metric
}

// Decision names an immutable policy and complete registry snapshot. A later
// family start invalidates using an old decision for a new incubation entry.
type Decision struct {
	ID, StrategyID, DeclarationSHA256, FamilySHA256, Policy string
	EvidenceClass                                           string
	Status, Stage, Minimum                                  string
	SearchTotal                                             int64
	Checks                                                  []Check
	Diagnostics                                             []Diagnostic
	At                                                      time.Time
}

// AdmitsIncubation excludes synthetic and legacy unclassified decisions from
// new exposure. Forward-only models have no historical performance admission.
func (d Decision) AdmitsIncubation() bool {
	return d.Stage == "incubation" && (d.Status == "passed" && d.EvidenceClass == "historical_market_unqualified" || d.Status == "forward_only" && d.EvidenceClass == "forward_only")
}

// Validate checks internal status/check consistency, not external provenance.
func (d Decision) Validate() error {
	if !strategytrial.ValidID(d.ID) || !strategyHash.MatchString(d.StrategyID) || !digest.MatchString(d.DeclarationSHA256) || !digest.MatchString(d.FamilySHA256) || d.Policy != "backtest-gate-v1" || d.At.IsZero() || len(d.Checks) == 0 || len(d.Checks) > 20000 || d.SearchTotal < 0 {
		return ErrInvalid
	}
	fail := false
	for _, c := range d.Checks {
		if c.Code == "" || c.Reason == "" || c.Passed && c.Skipped {
			return ErrInvalid
		}
		if !c.Passed && !c.Skipped {
			fail = true
		}
	}
	switch d.Status {
	case "failed":
		if !fail || d.Stage != "idea" {
			return ErrInvalid
		}
	case "passed", "forward_only":
		if fail || !d.AdmitsIncubation() {
			return ErrInvalid
		}
	default:
		return ErrInvalid
	}
	return nil
}

// Record pairs the stored declaration clock with its optional final decision.
type Record struct {
	Declaration Declaration
	DeclaredAt  time.Time
	Decision    *Decision
}

// Store keeps declarations and final decisions append-only and retry-safe.
type Store interface {
	DeclareStrategyGate(context.Context, Declaration) (Record, error)
	RecordStrategyGate(context.Context, Decision) (Record, error)
	StrategyGate(context.Context, string) (Record, error)
}
