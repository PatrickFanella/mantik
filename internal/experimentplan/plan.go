// Package experimentplan validates versioned synthetic experiment declarations.
// It owns no dataset loader, replay engine, storage, provider or execution path.
package experimentplan

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/domain"
	"git.subcult.tv/subculture-collective/mantik/internal/library"
)

const (
	RecipeSchema = "mantik-experiment-recipe-v1"
	NoActionID   = "no-action-control-v1"
	PassiveID    = "passive-mark-control-v1"
	CostModel    = "observed_quote_zero_cost_v1"
)

var identifier = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,80}$`)
var metricNames = []string{"observations", "actions", "realized_pnl_cents", "marked_pnl_cents"}

type Method struct {
	Entry             library.Entry           `json:"entry"`
	Strategy          *domain.StrategyVersion `json:"strategy,omitempty"`
	Safety            domain.Safety           `json:"safety"`
	Assumptions       []string                `json:"assumptions"`
	CostModel         string                  `json:"cost_model"`
	BenchmarkContract string                  `json:"benchmark_contract"`
	LiteratureSupport string                  `json:"literature_support"`
}

func MethodByID(id string) (Method, error) {
	e := library.FixtureEntry()
	m := Method{Entry: e, Safety: library.FixtureStrategy().Safety, CostModel: CostModel, BenchmarkContract: "independent_100000_cent_cash_account_v1", LiteratureSupport: "synthetic_fixture_only", Assumptions: []string{"DEMO quotes in integer cents", "zero fees and slippage", "one case per independent run", "no qualification credit"}}
	if id == NoActionID || id == PassiveID {
		m.Safety = domain.Safety{MaxNotionalCents: 100000, MaxHoldingSeconds: 86400, MaxQuoteAgeSeconds: 60}
		m.Assumptions = append(m.Assumptions, "nonexecuting control; stop and target fields are not applicable")
		m.Entry.ID, m.Entry.Kind, m.Entry.Implementation = id, "component", id
		m.Entry.Outputs = []string{"benchmark"}
		m.Entry.Evaluation = library.Recipe{ID: "control-evaluation", Version: "v1", Required: []library.Check{{ID: "dataset_quality", Version: "v1", Actions: []string{"benchmark"}}, {ID: "window_coverage", Version: "v1", Actions: []string{"benchmark"}}}}
		m.BenchmarkContract = "no_actions_zero_pnl_v1"
		if id == PassiveID {
			m.BenchmarkContract = "whole_units_first_quote_terminal_mark_v1"
			m.Assumptions = append(m.Assumptions, "mark-only benchmark; no orders or protective exit simulation", "whole units bought notionally at first quote; residual cash earns zero")
		}
	} else {
		s, err := library.Strategy(id)
		if err != nil {
			return Method{}, err
		}
		e, err := library.StrategyEntry(id)
		if err != nil {
			return Method{}, err
		}
		m.Entry, m.Strategy, m.Safety = e, &s, s.Safety
	}
	return m, nil
}

func Registry() (*library.Registry, error) {
	var entries []library.Entry
	for _, id := range builtinMethodIDs() {
		m, err := MethodByID(id)
		if err != nil {
			return nil, err
		}
		entries = append(entries, m.Entry)
	}
	return library.New(entries...)
}

func digest(value any) (string, error) {
	raw, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%x", sha256.Sum256(raw)), nil
}

type MethodPin struct {
	ID         string `json:"id"`
	SHA256     string `json:"sha256"`
	Definition Method `json:"definition"`
}

type Recipe struct {
	Schema         string      `json:"schema"`
	ID             string      `json:"id"`
	Version        string      `json:"version"`
	DatasetID      string      `json:"dataset_id"`
	DatasetVersion string      `json:"dataset_version"`
	DatasetSHA256  string      `json:"dataset_sha256"`
	WindowStart    time.Time   `json:"window_start"`
	WindowEnd      time.Time   `json:"window_end"`
	Cutoffs        []time.Time `json:"cutoffs"`
	RequiredKeys   []string    `json:"required_keys"`
	Methods        []MethodPin `json:"methods"`
	Benchmarks     []MethodPin `json:"benchmarks"`
	CapitalCents   int64       `json:"capital_cents"`
	CostModel      string      `json:"cost_model"`
	PlannedMetrics []string    `json:"planned_metrics"`
	SHA256         string      `json:"sha256"`
}

func Seal(r Recipe) (Recipe, error) {
	if err := r.validateDeclaration(); err != nil {
		return Recipe{}, err
	}
	r.SHA256 = ""
	raw, err := json.Marshal(r)
	if err != nil {
		return Recipe{}, err
	}
	var sealed Recipe
	if err := json.Unmarshal(raw, &sealed); err != nil {
		return Recipe{}, err
	}
	sealed.SHA256 = fmt.Sprintf("%x", sha256.Sum256(raw))
	return sealed, nil
}

func (r Recipe) Validate() error {
	sealed, err := Seal(r)
	if err != nil {
		return err
	}
	if r.SHA256 == "" || sealed.SHA256 != r.SHA256 {
		return errors.New("experiment recipe identity mismatch")
	}
	return nil
}

func (r Recipe) validateDeclaration() error {
	registry, err := Registry()
	if err != nil {
		return err
	}
	if r.Schema != RecipeSchema || !identifier.MatchString(r.ID) || !identifier.MatchString(r.Version) || !identifier.MatchString(r.DatasetID) || !identifier.MatchString(r.DatasetVersion) || !regexp.MustCompile(`^[0-9a-f]{64}$`).MatchString(r.DatasetSHA256) || r.CapitalCents != 100000 || r.CostModel != CostModel || r.WindowStart.IsZero() || r.WindowEnd.Before(r.WindowStart) || r.WindowEnd.Sub(r.WindowStart) > 24*time.Hour {
		return errors.New("unsupported or incomplete experiment declaration")
	}
	if len(r.Cutoffs) == 0 || len(r.Cutoffs) > 100 || !r.Cutoffs[0].Equal(r.WindowStart) || !r.Cutoffs[len(r.Cutoffs)-1].Equal(r.WindowEnd) {
		return errors.New("cutoffs must span the declared window")
	}
	var previous time.Time
	for _, at := range r.Cutoffs {
		if at.IsZero() || !previous.IsZero() && !at.After(previous) {
			return errors.New("replay cutoffs must be nonzero and strictly increasing")
		}
		previous = at.UTC()
	}
	if len(r.RequiredKeys) == 0 || len(r.RequiredKeys) > 10000 {
		return errors.New("required inputs must be declared")
	}
	seen := map[string]bool{}
	for _, key := range r.RequiredKeys {
		if !identifier.MatchString(key) || seen[key] {
			return errors.New("invalid or duplicate required input")
		}
		seen[key] = true
	}
	if !slices.Equal(r.PlannedMetrics, metricNames) {
		return errors.New("unsupported planned metrics")
	}
	if len(r.Methods) == 0 || len(r.Methods) > 2 || len(r.Benchmarks) != 2 {
		return errors.New("declare methods and both benchmark controls")
	}
	seen = map[string]bool{}
	for _, group := range [][]MethodPin{r.Methods, r.Benchmarks} {
		for _, pin := range group {
			if _, err := registry.Resolve(pin.ID, library.ContractVersion); err != nil {
				return err
			}
			m, err := MethodByID(pin.ID)
			if err != nil {
				return err
			}
			hash, err := digest(m)
			if err != nil {
				return err
			}
			declaredHash, err := digest(pin.Definition)
			if err != nil {
				return err
			}
			if hash != pin.SHA256 || declaredHash != hash || seen[pin.ID] {
				return errors.New("incompatible or duplicate method pin")
			}
			seen[pin.ID] = true
		}
	}
	for _, pin := range r.Methods {
		if pin.ID == NoActionID || pin.ID == PassiveID {
			return errors.New("control cannot replace a strategy")
		}
	}
	if !seen[NoActionID] || !seen[PassiveID] {
		return errors.New("missing benchmark binding")
	}
	return nil
}

// Metrics returns the required metric contract without sharing mutable slices.
func Metrics() []string { return slices.Clone(metricNames) }
