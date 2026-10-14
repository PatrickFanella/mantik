// Package experiment declares synthetic comparisons before execution. It owns
// no service, provider, allocator, qualification, or best-result pointer.
package experiment

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/domain"
	"git.subcult.tv/subculture-collective/mantik/internal/library"
	"git.subcult.tv/subculture-collective/mantik/internal/replay"
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
	for _, id := range []string{library.FixtureStrategy().ID, library.HalfSizeID, NoActionID, PassiveID} {
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
	return replay.Digest(string(raw)), nil
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
	sealed.SHA256 = replay.Digest(string(raw))
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
	var clock replay.Clock
	for _, at := range r.Cutoffs {
		if err := clock.Advance(at); err != nil {
			return err
		}
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

func DefaultRecipe(d replay.Dataset) (Recipe, error) {
	if err := d.Validate(); err != nil {
		return Recipe{}, err
	}
	records := slices.Clone(d.Records)
	slices.SortFunc(records, func(a, b replay.Record) int { return a.VisibleAt().Compare(b.VisibleAt()) })
	r := Recipe{Schema: RecipeSchema, ID: "synthetic-baselines", Version: "v1", DatasetID: d.ID, DatasetVersion: d.Version, DatasetSHA256: d.SHA256, WindowStart: records[0].VisibleAt(), WindowEnd: records[len(records)-1].VisibleAt(), CapitalCents: 100000, CostModel: CostModel, PlannedMetrics: slices.Clone(metricNames)}
	seen := map[string]bool{}
	for _, record := range records {
		if !seen[record.Key] {
			r.RequiredKeys = append(r.RequiredKeys, record.Key)
			seen[record.Key] = true
		}
		if len(r.Cutoffs) == 0 || !record.VisibleAt().Equal(r.Cutoffs[len(r.Cutoffs)-1]) {
			r.Cutoffs = append(r.Cutoffs, record.VisibleAt())
		}
	}
	slices.Sort(r.RequiredKeys)
	for _, id := range []string{library.FixtureStrategy().ID, library.HalfSizeID, NoActionID, PassiveID} {
		m, err := MethodByID(id)
		if err != nil {
			return Recipe{}, err
		}
		hash, err := digest(m)
		if err != nil {
			return Recipe{}, err
		}
		pin := MethodPin{ID: id, SHA256: hash, Definition: m}
		if m.Strategy != nil {
			r.Methods = append(r.Methods, pin)
		} else {
			r.Benchmarks = append(r.Benchmarks, pin)
		}
	}
	return Seal(r)
}

type Metrics struct {
	Observations     int64 `json:"observations"`
	Actions          int64 `json:"actions"`
	RealizedPnLCents int64 `json:"realized_pnl_cents"`
	MarkedPnLCents   int64 `json:"marked_pnl_cents"`
}

type MethodResult struct {
	MethodID     string         `json:"method_id"`
	MethodSHA256 string         `json:"method_sha256"`
	Metrics      Metrics        `json:"metrics"`
	Replay       *replay.Result `json:"replay,omitempty"`
}

type Attempt struct {
	ID            string         `json:"id"`
	RecipeSHA256  string         `json:"recipe_sha256"`
	InputSHA256   string         `json:"input_sha256"`
	Status        string         `json:"status"`
	ErrorCode     string         `json:"error_code,omitempty"`
	EvidenceClass string         `json:"evidence_class"`
	Qualified     bool           `json:"qualified"`
	Results       []MethodResult `json:"results"`
}

// Execute retains no winning method or qualification state. Valid declarations
// produce a terminal attempt even when data admission or execution fails.
func Execute(ctx context.Context, id string, r Recipe, d replay.Dataset) (Attempt, error) {
	if !identifier.MatchString(id) {
		return Attempt{}, errors.New("invalid attempt id")
	}
	if err := r.Validate(); err != nil {
		return Attempt{}, err
	}
	inputHash, err := digest(d)
	if err != nil {
		return Attempt{}, err
	}
	a := Attempt{ID: id, RecipeSHA256: r.SHA256, InputSHA256: inputHash, Status: "failed", EvidenceClass: "synthetic_unqualified", Results: []MethodResult{}}
	fail := func(code string) (Attempt, error) { a.ErrorCode = code; return a, nil }
	if err := ctx.Err(); err != nil {
		return fail("cancelled")
	}
	if err := d.Validate(); err != nil {
		return fail("dataset_rejected")
	}
	if d.ID != r.DatasetID || d.Version != r.DatasetVersion || d.SHA256 != r.DatasetSHA256 {
		return fail("dataset_pin_mismatch")
	}
	quotes, err := admittedQuotes(r, d)
	if err != nil {
		return fail("input_admission_failed")
	}
	for _, pin := range r.Methods {
		pins, err := replay.StrategyPins(pin.ID)
		if err != nil {
			return fail("method_unavailable")
		}
		run, err := replay.Run(ctx, d, pins, r.Cutoffs)
		if err != nil {
			return fail("replay_failed")
		}
		last := run.Frames[len(run.Frames)-1]
		if last.AdmissionBlocked {
			return fail("quality_blocked")
		}
		metrics := Metrics{Observations: last.Snapshot.ObservationCount}
		for _, event := range last.Snapshot.Events {
			if event.Outcome != nil {
				metrics.Actions++
				metrics.RealizedPnLCents += event.Outcome.RealizedPnLCents
			}
		}
		metrics.MarkedPnLCents = metrics.RealizedPnLCents
		if c := last.Snapshot.State.Case; c.Entry != nil && c.Exit == nil {
			metrics.MarkedPnLCents += (quotes[len(quotes)-1].PriceCents - c.Entry.PriceCents) * c.Entry.Quantity
		}
		a.Results = append(a.Results, MethodResult{pin.ID, pin.SHA256, metrics, &run})
	}
	for _, pin := range r.Benchmarks {
		m, _ := MethodByID(pin.ID)
		if err := m.Entry.Admit("benchmark", []domain.EvaluationResult{{Check: "dataset_quality", Version: "v1", Passed: true}, {Check: "window_coverage", Version: "v1", Passed: true}}); err != nil {
			return fail("benchmark_admission_failed")
		}
		metrics := Metrics{Observations: int64(len(quotes))}
		if pin.ID == PassiveID {
			quantity := r.CapitalCents / quotes[0].PriceCents
			metrics.MarkedPnLCents = (quotes[len(quotes)-1].PriceCents - quotes[0].PriceCents) * quantity
		}
		a.Results = append(a.Results, MethodResult{MethodID: pin.ID, MethodSHA256: pin.SHA256, Metrics: metrics})
	}
	a.Status = "completed"
	return a, nil
}

func admittedQuotes(r Recipe, d replay.Dataset) ([]domain.Observation, error) {
	var quotes []domain.Observation
	seen := map[string]bool{}
	for _, record := range d.Records {
		if record.VisibleAt().Before(r.WindowStart) || record.VisibleAt().After(r.WindowEnd) {
			return nil, errors.New("input outside declared window")
		}
		o, err := record.ExecutionObservation()
		if err != nil {
			return nil, err
		}
		if seen[record.Key] {
			continue
		}
		seen[record.Key] = true
		if o.ReceivedAt.Sub(o.OccurredAt) > 60*time.Second {
			return nil, errors.New("stale benchmark input")
		}
		quotes = append(quotes, o)
	}
	if len(seen) != len(r.RequiredKeys) {
		return nil, errors.New("unexpected input coverage")
	}
	for _, key := range r.RequiredKeys {
		if !seen[key] {
			return nil, fmt.Errorf("missing input %q", key)
		}
	}
	slices.SortFunc(quotes, func(a, b domain.Observation) int {
		if n := a.ReceivedAt.Compare(b.ReceivedAt); n != 0 {
			return n
		}
		return stringsCompare(a.ID, b.ID)
	})
	for i := 1; i < len(quotes); i++ {
		if !quotes[i].OccurredAt.After(quotes[i-1].OccurredAt) {
			return nil, errors.New("unordered benchmark input")
		}
	}
	return quotes, nil
}

func stringsCompare(a, b string) int {
	if a < b {
		return -1
	}
	if a > b {
		return 1
	}
	return 0
}
