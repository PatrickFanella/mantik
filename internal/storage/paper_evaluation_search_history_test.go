package storage

import (
	"errors"
	"reflect"
	"testing"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/papereconomics"
	"git.subcult.tv/subculture-collective/mantik/internal/paperstrategy"
)

func searchDeclarationCommand(t *testing.T, m *Memory) (PaperEvaluationDeclarationCommand, time.Time) {
	t.Helper()
	c, at := declarationFixture(t, m)
	c.Recipe = PaperSearchHistoryEvaluationRecipe
	c.Plan.Holdout = &PaperEvaluationHoldoutPlan{Method: PaperHoldoutMethod, SplitAt: c.Plan.WindowStart.Add(30 * time.Minute), EmbargoSeconds: 60, MinimumTrainingOccurrences: 2, MinimumHoldoutOccurrences: 2}
	c.Plan.BenchmarkMethod = papereconomics.BenchmarkMethod
	c.Plan.FeatureCausalityMethod = PaperFeatureCausalityMethod
	c.Plan.SampleDesign = &PaperEvaluationSampleDesignPlan{Method: PaperDisjointSampleMethod, MinimumDisjointOccurrences: c.Plan.MinIndependentDecisions}
	c.Plan.SearchMultiplicity = &PaperEvaluationSearchPlan{Method: PaperSearchHistoryMethod, MaximumSearchedStrategies: len(c.Plan.Variants)}
	return c, at
}

func TestSearchBoundIsProspectiveAndPreservesSampleDeclarations(t *testing.T) {
	m := NewMemory()
	c, at := searchDeclarationCommand(t, m)
	legacy := clone(c)
	legacy.Recipe, legacy.Plan.SearchMultiplicity = PaperSampleDesignEvaluationRecipe, nil
	old, err := m.DeclarePaperEvaluation(t.Context(), legacy, func() time.Time { return at.Add(time.Second) })
	if err != nil || old.Record.Schema != "mantik-owned-paper-evaluation-declaration-v7" {
		t.Fatal(old, err)
	}
	c.ID, c.Plan.FamilyID = "search-plan", "search-family"
	sampleOnly := clone(c)
	sampleOnly.Recipe = PaperSampleDesignEvaluationRecipe
	if sampleOnly.Validate() == nil {
		t.Fatal("sample-design recipe accepted a search bound")
	}
	got, err := m.DeclarePaperEvaluation(t.Context(), c, func() time.Time { return at.Add(2 * time.Second) })
	if err != nil || got.Record.Schema != "mantik-owned-paper-evaluation-declaration-v8" {
		t.Fatal(got, err)
	}
	clock := func() time.Time { panic("retry or conflict sampled clock") }
	if retry, err := m.DeclarePaperEvaluation(t.Context(), c, clock); err != nil || !retry.Duplicate || !reflect.DeepEqual(got.Record, retry.Record) {
		t.Fatal(retry, err)
	}
	all, err := m.PaperEvaluationDeclarations(t.Context())
	if err != nil || len(all.Records) != 2 {
		t.Fatal(all, err)
	}
	for _, record := range all.Records {
		if record.Command.ID == old.Record.Command.ID && !reflect.DeepEqual(record, old.Record) {
			t.Fatal("legacy sample declaration changed")
		}
	}
	strategies, _ := m.PaperStrategies(t.Context())
	policies, _ := m.AllocationPolicies(t.Context())
	if _, err := paperEvaluationDeclarationVersion(c, strategies, policies, got.Record.DeclaredAt, "mantik-owned-paper-evaluation-declaration-v7"); !errors.Is(err, ErrPaperEvaluationDeclarationInvalid) {
		t.Fatal("search bound downgrade accepted", err)
	}
	for name, change := range map[string]func(*PaperEvaluationDeclarationCommand){
		"missing":        func(c *PaperEvaluationDeclarationCommand) { c.Plan.SearchMultiplicity = nil },
		"unknown method": func(c *PaperEvaluationDeclarationCommand) { c.Plan.SearchMultiplicity.Method = "unknown" },
		"below variants": func(c *PaperEvaluationDeclarationCommand) {
			c.Plan.SearchMultiplicity.MaximumSearchedStrategies = len(c.Plan.Variants) - 1
		},
		"above registry cap": func(c *PaperEvaluationDeclarationCommand) {
			c.Plan.SearchMultiplicity.MaximumSearchedStrategies = paperstrategy.MaxDefinitions + 1
		},
		"missing sample design": func(c *PaperEvaluationDeclarationCommand) {
			c.Plan.SampleDesign = nil
		},
	} {
		t.Run(name, func(t *testing.T) {
			bad := clone(c)
			change(&bad)
			if bad.Validate() == nil {
				t.Fatal("invalid search bound accepted")
			}
		})
	}
	c.Plan.SearchMultiplicity.MaximumSearchedStrategies++
	if _, err := m.DeclarePaperEvaluation(t.Context(), c, clock); !errors.Is(err, ErrPaperEvaluationDeclarationConflict) {
		t.Fatal("retrospective bound change accepted", err)
	}
}

func TestRegisteredSearchHistoryCountsUndeclaredStrategiesAtTheCut(t *testing.T) {
	m := NewMemory()
	c, at := searchDeclarationCommand(t, m)
	d, err := m.DeclarePaperEvaluation(t.Context(), c, func() time.Time { return at.Add(time.Second) })
	if err != nil {
		t.Fatal(err)
	}
	e := PaperEvaluationEvidence{ReceiverID: c.ReceiverID, Declaration: d.Record, AsOf: at.Add(time.Hour)}
	strategies, _ := m.PaperStrategies(t.Context())
	within, err := ownedSearchHistory(e, strategies, e.AsOf)
	if err != nil || within.SearchedStrategies != 2 || within.Status != "unavailable" || within.ReasonCode != "search_count_without_multiplicity_adjusted_statistic" || within.MultiplicityAdjusted || within.EvaluationComplete || within.AdmissionAvailable || within.ExecutionAvailable || within.QualificationCredit {
		t.Fatal("bounded count became a multiplicity result", within, err)
	}
	extra := strategyDefinition("undeclared-v1")
	if _, err := m.RegisterPaperStrategy(t.Context(), paperstrategy.Command{ReceiverID: c.ReceiverID, Definition: extra}, func() time.Time { return at.Add(time.Minute) }); err != nil {
		t.Fatal(err)
	}
	strategies, _ = m.PaperStrategies(t.Context())
	before, err := ownedSearchHistory(e, strategies, at.Add(time.Minute-time.Nanosecond))
	if err != nil || before.SearchedStrategies != 2 || before.Status != "unavailable" {
		t.Fatal("later registration entered an earlier cut", before, err)
	}
	exceeded, err := ownedSearchHistory(e, strategies, e.AsOf)
	if err != nil || exceeded.SearchedStrategies != 3 || exceeded.Status != "failed" || exceeded.ReasonCode != "registered_search_exceeds_predeclared_bound" {
		t.Fatal("undeclared registered search was omitted", exceeded, err)
	}
	for _, s := range exceeded.Strategies {
		if s.Declared != (s.StrategyID != "undeclared-v1") {
			t.Fatal("declared membership misattributed", exceeded.Strategies)
		}
	}
	if retained, err := retainedSearchHistory(e, &exceeded); err != nil || !reflect.DeepEqual(retained, exceeded) {
		t.Fatal("retained envelope did not rederive", err)
	}
	r := PaperEvaluationAssessment{Status: "incomplete", Checks: []PaperEvaluationCheck{{StrategyID: "baseline-v1", Name: "search_multiplicity", Status: "unavailable"}, {StrategyID: "baseline-v1", Name: "leakage", Status: "unavailable"}}}
	applySearchHistory(&r, exceeded)
	if r.Status != "blocked" || r.Checks[0].Status != "failed" || r.Checks[1].Status != "unavailable" {
		t.Fatal("failed search bound did not block only its check", r)
	}
	for name, change := range map[string]func(*PaperEvaluationSearchHistory){
		"omitted":    func(h *PaperEvaluationSearchHistory) { h.Strategies = h.Strategies[1:] },
		"duplicate":  func(h *PaperEvaluationSearchHistory) { h.Strategies = append(h.Strategies, h.Strategies[0]) },
		"undeclared": func(h *PaperEvaluationSearchHistory) { h.Strategies[0].Declared = !h.Strategies[0].Declared },
		"future":     func(h *PaperEvaluationSearchHistory) { h.Strategies[0].RegisteredAt = e.AsOf.Add(time.Second) },
		"status":     func(h *PaperEvaluationSearchHistory) { h.Status = "unavailable" },
		"adjusted":   func(h *PaperEvaluationSearchHistory) { h.MultiplicityAdjusted = true },
	} {
		t.Run(name, func(t *testing.T) {
			bad := clone(exceeded)
			change(&bad)
			if _, err := retainedSearchHistory(e, &bad); !errors.Is(err, ErrPaperEvaluationAssessmentInvalid) {
				t.Fatal("tampered search history accepted", err)
			}
		})
	}
}
