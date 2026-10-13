// Package library defines versioned method contracts and deterministic admission.
// It has no model, storage, or execution access.
package library

import (
	"fmt"
	"slices"

	"git.subcult.tv/subculture-collective/mantik/internal/domain"
)

const ContractVersion = "mantik-library-v1"

type Input struct {
	Kind   string
	Source string
	Schema string
}

type Check struct {
	ID      string
	Version string
	Actions []string
}

// Recipe keeps mandatory checks separate from tools an investigator may choose.
type Recipe struct {
	ID            string
	Version       string
	Required      []Check
	OptionalTools []string
}

type Entry struct {
	ID                 string
	ContractVersion    string
	Kind               string
	Implementation     string
	Inputs             []Input
	Instruments        []string
	Outputs            []string
	Dependencies       []string
	EvidenceClass      string
	SafetyRequirements []string
	Evaluation         Recipe
}

// AcceptsQuote binds the implemented quote schema to declared source/instrument
// support. Other schemas need an explicit adapter before they can supply input.
func (e Entry) AcceptsQuote(o domain.Observation) bool {
	if !slices.Contains(e.Instruments, o.Symbol) {
		return false
	}
	for _, input := range e.Inputs {
		if input.Kind == o.Kind && input.Source == o.Source && input.Schema == "observation-quote-cents-v1" {
			return true
		}
	}
	return false
}

var safetyRequirements = []string{"max_notional", "stop", "target", "max_holding", "max_quote_age"}

// Registry is immutable after construction. Dependencies must name exact versions.
type Registry struct{ entries map[string]Entry }

func New(entries ...Entry) (*Registry, error) {
	r := &Registry{entries: make(map[string]Entry)}
	for _, e := range entries {
		if err := validate(e); err != nil {
			return nil, err
		}
		if _, exists := r.entries[e.ID]; exists {
			return nil, fmt.Errorf("duplicate library version %q", e.ID)
		}
		r.entries[e.ID] = clone(e)
	}
	visiting, visited := map[string]bool{}, map[string]bool{}
	var visit func(string) error
	visit = func(id string) error {
		if visiting[id] {
			return fmt.Errorf("cyclic library dependency %q", id)
		}
		if visited[id] {
			return nil
		}
		e, exists := r.entries[id]
		if !exists {
			return fmt.Errorf("missing library dependency %q", id)
		}
		visiting[id] = true
		for _, dependency := range e.Dependencies {
			if err := visit(dependency); err != nil {
				return err
			}
		}
		visiting[id], visited[id] = false, true
		return nil
	}
	for id := range r.entries {
		if err := visit(id); err != nil {
			return nil, err
		}
	}
	return r, nil
}

func (r *Registry) Resolve(id, version string) (Entry, error) {
	if version != ContractVersion {
		return Entry{}, fmt.Errorf("incompatible library contract %q", version)
	}
	e, ok := r.entries[id]
	if !ok {
		return Entry{}, fmt.Errorf("unknown library version %q", id)
	}
	return clone(e), nil
}

func validate(e Entry) error {
	if e.ID == "" || e.ContractVersion != ContractVersion || (e.Kind != "strategy" && e.Kind != "component") || e.Implementation == "" || len(e.Inputs) == 0 || len(e.Instruments) == 0 || len(e.Outputs) == 0 {
		return fmt.Errorf("incomplete or incompatible library contract %q", e.ID)
	}
	if !slices.Contains([]string{"synthetic_unqualified", "stress", "historical", "prospective"}, e.EvidenceClass) {
		return fmt.Errorf("unknown evidence class for %q", e.ID)
	}
	for _, input := range e.Inputs {
		if input.Kind == "" || input.Source == "" || input.Schema == "" {
			return fmt.Errorf("incomplete typed input for %q", e.ID)
		}
	}
	for _, values := range [][]string{e.Instruments, e.Outputs, e.Dependencies, e.SafetyRequirements, e.Evaluation.OptionalTools} {
		seen := map[string]bool{}
		for _, value := range values {
			if value == "" || seen[value] {
				return fmt.Errorf("empty or duplicate declaration for %q", e.ID)
			}
			seen[value] = true
		}
	}
	for _, requirement := range safetyRequirements {
		if !slices.Contains(e.SafetyRequirements, requirement) {
			return fmt.Errorf("missing safety requirement %q for %q", requirement, e.ID)
		}
	}
	if e.Evaluation.ID == "" || e.Evaluation.Version == "" || len(e.Evaluation.Required) == 0 {
		return fmt.Errorf("missing mandatory evaluation recipe for %q", e.ID)
	}
	seen := map[string]bool{}
	for _, check := range e.Evaluation.Required {
		if check.ID == "" || check.Version == "" || len(check.Actions) == 0 || seen[check.ID] {
			return fmt.Errorf("invalid mandatory check for %q", e.ID)
		}
		seen[check.ID] = true
		for _, action := range check.Actions {
			if !slices.Contains(e.Outputs, action) {
				return fmt.Errorf("check action %q is not declared by %q", action, e.ID)
			}
		}
	}
	for _, action := range e.Outputs {
		covered := false
		for _, check := range e.Evaluation.Required {
			covered = covered || slices.Contains(check.Actions, action)
		}
		if !covered {
			return fmt.Errorf("output %q lacks mandatory evaluations", action)
		}
	}
	return nil
}

// Admit fails closed for unknown actions, absent checks, duplicates, and failures.
// Optional tools cannot substitute for a required check.
func (e Entry) Admit(action string, results []domain.EvaluationResult) error {
	if err := validate(e); err != nil {
		return err
	}
	if !slices.Contains(e.Outputs, action) {
		return fmt.Errorf("unsupported output %q", action)
	}
	seen := map[string]bool{}
	for _, result := range results {
		if result.Check == "" || seen[result.Check] || !result.Passed {
			return fmt.Errorf("failed or duplicate evaluation %q", result.Check)
		}
		known := false
		version := result.Version
		if version == "" {
			version = "v1"
		}
		for _, check := range e.Evaluation.Required {
			known = known || check.ID == result.Check && check.Version == version
		}
		// Retained bootstrap results without a version denote v1 only.
		if !known {
			return fmt.Errorf("unknown or incompatible evaluation %q", result.Check)
		}
		seen[result.Check] = true
	}
	for _, check := range e.Evaluation.Required {
		if slices.Contains(check.Actions, action) && !seen[check.ID] {
			return fmt.Errorf("missing mandatory evaluation %q", check.ID)
		}
	}
	return nil
}

func clone(e Entry) Entry {
	e.Inputs = slices.Clone(e.Inputs)
	e.Instruments = slices.Clone(e.Instruments)
	e.Outputs = slices.Clone(e.Outputs)
	e.Dependencies = slices.Clone(e.Dependencies)
	e.SafetyRequirements = slices.Clone(e.SafetyRequirements)
	e.Evaluation.Required = slices.Clone(e.Evaluation.Required)
	for i := range e.Evaluation.Required {
		e.Evaluation.Required[i].Actions = slices.Clone(e.Evaluation.Required[i].Actions)
	}
	e.Evaluation.OptionalTools = slices.Clone(e.Evaluation.OptionalTools)
	return e
}
