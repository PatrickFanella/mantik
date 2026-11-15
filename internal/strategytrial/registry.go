// Package strategytrial records daily-engine research attempts. A start counts
// toward search multiplicity even when execution never produces an outcome.
package strategytrial

import (
	"context"
	"encoding/json"
	"errors"
	"regexp"
	"time"
)

var (
	ErrInvalid  = errors.New("invalid strategy trial record")
	ErrConflict = errors.New("strategy trial record conflicts with retained history")
	ErrNotFound = errors.New("strategy trial not found")
	idText      = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._-]{0,127}$`)
	familyText  = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,63}$`)
	strategyID  = regexp.MustCompile(`^strategy-[a-f0-9]{64}$`)
	digest      = regexp.MustCompile(`^[a-f0-9]{64}$`)
	number      = regexp.MustCompile(`^-?(0|[1-9][0-9]{0,29})(\.[0-9]{1,18})?$`)
)

// Start is the immutable declaration saved before calling the engine. Spec
// contains the strategy version and parameters; Config contains the engine
// settings. DataSHA256 identifies the caller's input manifest, not verified
// data coverage. Parents names earlier attempt IDs, including failed ones.
type Start struct {
	ID            string          `json:"id"`
	Family        string          `json:"family"`
	StrategyID    string          `json:"strategy_id"`
	Spec          json.RawMessage `json:"spec"`
	Config        json.RawMessage `json:"config"`
	DataSHA256    string          `json:"data_sha256"`
	EvidenceClass string          `json:"evidence_class"`
	Parents       []string        `json:"parents"`
	StartedAt     time.Time       `json:"started_at"`
}

// Validate checks a declaration's shape, not its asserted provenance.
func (s Start) Validate() error {
	if !ValidID(s.ID) || !ValidFamily(s.Family) || !strategyID.MatchString(s.StrategyID) || !digest.MatchString(s.DataSHA256) || !validTime(s.StartedAt) || !object(s.Spec) || !object(s.Config) || len(s.Parents) > 16 || (s.EvidenceClass != "synthetic_fixture" && s.EvidenceClass != "historical_market_unqualified") {
		return ErrInvalid
	}
	seen := map[string]bool{}
	for _, p := range s.Parents {
		if !ValidID(p) || p == s.ID || seen[p] {
			return ErrInvalid
		}
		seen[p] = true
	}
	return nil
}

// ValidID reports whether an attempt ID is valid.
func ValidID(id string) bool { return idText.MatchString(id) }

// ValidFamily reports whether a stable search-family name is valid.
func ValidFamily(family string) bool { return familyText.MatchString(family) }
func validTime(at time.Time) bool    { return !at.IsZero() && at.Year() >= 1900 && at.Year() <= 9999 }
func object(raw []byte) bool {
	var fields map[string]json.RawMessage
	return len(raw) > 0 && len(raw) <= 64<<10 && json.Unmarshal(raw, &fields) == nil && len(fields) > 0
}

// DailyReturn is one session's time-weighted return and marked equity, as
// decimal text emitted by the engine. Dates are strictly increasing per trial.
type DailyReturn struct {
	Date   string `json:"date"`
	Return string `json:"return"`
	Equity string `json:"equity"`
}

// Validate checks the session date and exact decimal values.
func (d DailyReturn) Validate() error {
	at, err := time.Parse(time.DateOnly, d.Date)
	if err != nil || at.Year() < 1900 || !number.MatchString(d.Return) || !number.MatchString(d.Equity) {
		return ErrInvalid
	}
	return nil
}

// Outcome closes an attempt once. An attempt without an outcome is unfinished;
// a recovery owner may append crashed or abandoned after it has stopped it.
type Outcome struct {
	Status     string    `json:"status"`
	FinishedAt time.Time `json:"finished_at"`
}

// Validate checks the terminal status and clock.
func (o Outcome) Validate() error {
	if !validTime(o.FinishedAt) {
		return ErrInvalid
	}
	switch o.Status {
	case "completed", "failed", "crashed", "abandoned":
		return nil
	}
	return ErrInvalid
}

// Record is a declaration followed by its retained daily returns and optional
// terminal outcome. No status here grants qualification or trading authority.
type Record struct {
	Start   Start         `json:"start"`
	Returns []DailyReturn `json:"returns"`
	Outcome *Outcome      `json:"outcome,omitempty"`
}

// Store is the append-only daily-engine registry. StartStrategyTrial returns
// true only for a new attempt. Exact write retries are idempotent; changed
// retries conflict. Every start counts, including unfinished attempts.
type Store interface {
	StartStrategyTrial(context.Context, Start) (bool, error)
	AppendStrategyTrialReturn(context.Context, string, DailyReturn) error
	FinishStrategyTrial(context.Context, string, Outcome) error
	StrategyTrial(context.Context, string) (Record, error)
	StrategyTrialSearchTotal(context.Context, string) (int64, error)
}
