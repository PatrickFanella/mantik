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
	"git.subcult.tv/subculture-collective/mantik/internal/experimentplan"
	"git.subcult.tv/subculture-collective/mantik/internal/library"
	"git.subcult.tv/subculture-collective/mantik/internal/replay"
)

const (
	RecipeSchema = experimentplan.RecipeSchema
	NoActionID   = experimentplan.NoActionID
	PassiveID    = experimentplan.PassiveID
	CostModel    = experimentplan.CostModel
)

var identifier = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,80}$`)

type Method = experimentplan.Method
type MethodPin = experimentplan.MethodPin
type Recipe = experimentplan.Recipe

func MethodByID(id string) (Method, error) { return experimentplan.MethodByID(id) }
func Registry() (*library.Registry, error) { return experimentplan.Registry() }
func Seal(r Recipe) (Recipe, error)        { return experimentplan.Seal(r) }
func digest(value any) (string, error) {
	raw, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	return replay.Digest(string(raw)), nil
}

func DefaultRecipe(d replay.Dataset) (Recipe, error) {
	if err := d.Validate(); err != nil {
		return Recipe{}, err
	}
	records := slices.Clone(d.Records)
	slices.SortFunc(records, func(a, b replay.Record) int { return a.VisibleAt().Compare(b.VisibleAt()) })
	r := Recipe{Schema: RecipeSchema, ID: "synthetic-baselines", Version: "v1", DatasetID: d.ID, DatasetVersion: d.Version, DatasetSHA256: d.SHA256, WindowStart: records[0].VisibleAt(), WindowEnd: records[len(records)-1].VisibleAt(), CapitalCents: 100000, CostModel: CostModel, PlannedMetrics: experimentplan.Metrics()}
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
	catalog, err := BuiltinCatalog()
	if err != nil {
		return Recipe{}, err
	}
	for _, pin := range catalog.Methods {
		if pin.Definition.Strategy != nil {
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
