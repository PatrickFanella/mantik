package storage

import (
	"errors"
	"reflect"
	"slices"
	"testing"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/papereconomics"
)

func TestDisjointSamplePackingUsesClosedWindowsAndEarliestFinish(t *testing.T) {
	at := time.Date(2026, 10, 21, 14, 0, 0, 0, time.UTC)
	window := func(id string, start, end time.Duration, status string) PaperEvaluationSampleWindow {
		return PaperEvaluationSampleWindow{DecisionID: id, Start: at.Add(start), End: at.Add(end), Status: status}
	}
	input := []PaperEvaluationSampleWindow{
		window("long", 0, 10*time.Second, "eligible_observed_window"),
		window("early-b", 0, time.Second, "eligible_observed_window"),
		window("early-a", 0, time.Second, "eligible_observed_window"),
		window("touching", time.Second, 2*time.Second, "eligible_observed_window"),
		window("after", time.Second+time.Nanosecond, 2*time.Second, "eligible_observed_window"),
		window("missing-label", 3*time.Second, 4*time.Second, "outcome_unavailable"),
		window("not-closed", 5*time.Second, 6*time.Second, "window_not_closed_at_cut"),
	}
	before := clone(input)
	first := PaperEvaluationSampleVariant{Windows: clone(input)}
	packDisjointSampleWindows(&first)
	if first.ObservedWindows != 5 || first.DisjointWindows != 2 || first.OverlappingWindows != 3 || first.UnavailableWindows != 2 {
		t.Fatal(first)
	}
	selected := []string{}
	for _, w := range first.Windows {
		if w.Status == "selected_disjoint_window" {
			selected = append(selected, w.DecisionID)
		}
	}
	if !reflect.DeepEqual(selected, []string{"early-a", "after"}) {
		t.Fatal("touching/overlapping windows counted or tie order changed", selected)
	}
	if !reflect.DeepEqual(input, before) {
		t.Fatal("packing mutated its source slice")
	}
	slices.Reverse(input)
	second := PaperEvaluationSampleVariant{Windows: input}
	packDisjointSampleWindows(&second)
	if !reflect.DeepEqual(first, second) {
		t.Fatal("packing depends on source order or mutated unrelated input")
	}
}

func TestSampleDesignIsProspectiveAndPreservesCausalDeclarations(t *testing.T) {
	m := NewMemory()
	c, at := declarationFixture(t, m)
	c.Recipe = PaperCausalEvaluationRecipe
	c.Plan.Holdout = &PaperEvaluationHoldoutPlan{Method: PaperHoldoutMethod, SplitAt: c.Plan.WindowStart.Add(30 * time.Minute), EmbargoSeconds: 60, MinimumTrainingOccurrences: 2, MinimumHoldoutOccurrences: 2}
	c.Plan.BenchmarkMethod = papereconomics.BenchmarkMethod
	c.Plan.FeatureCausalityMethod = PaperFeatureCausalityMethod
	legacy, err := m.DeclarePaperEvaluation(t.Context(), c, func() time.Time { return at.Add(time.Second) })
	if err != nil {
		t.Fatal(err)
	}
	c.ID, c.Plan.FamilyID = "sample-plan", "sample-family"
	c.Plan.SampleDesign = &PaperEvaluationSampleDesignPlan{Method: PaperDisjointSampleMethod, MinimumDisjointOccurrences: c.Plan.MinIndependentDecisions}
	if c.Validate() == nil {
		t.Fatal("causal-only recipe accepted new sample design")
	}
	c.Recipe = PaperSampleDesignEvaluationRecipe
	got, err := m.DeclarePaperEvaluation(t.Context(), c, func() time.Time { return at.Add(2 * time.Second) })
	if err != nil || got.Record.Schema != "mantik-owned-paper-evaluation-declaration-v7" {
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
	found := false
	for _, record := range all.Records {
		if record.Command.ID == legacy.Record.Command.ID {
			found = reflect.DeepEqual(record, legacy.Record)
		}
	}
	if !found {
		t.Fatal("legacy causal declaration changed")
	}
	strategies, _ := m.PaperStrategies(t.Context())
	policies, _ := m.AllocationPolicies(t.Context())
	short := clone(c)
	short.ID, short.Plan.FamilyID = "short-window-plan", "short-window-family"
	short.Plan.OutcomeDeadline = short.Plan.WindowEnd.Add(time.Duration(strategies.Records[0].Command.Definition.Safety.MaxHoldingSeconds) * time.Second)
	if short.Validate() != nil {
		t.Fatal("deadline fixture is invalid before strategy binding")
	}
	if _, err := m.DeclarePaperEvaluation(t.Context(), short, func() time.Time { return at.Add(3 * time.Second) }); !errors.Is(err, ErrPaperEvaluationDeclarationConflict) {
		t.Fatal("observation deadline omitted the quote acceptance window", err)
	}
	if _, err := paperEvaluationDeclarationVersion(c, strategies, policies, got.Record.DeclaredAt, "mantik-owned-paper-evaluation-declaration-v6"); !errors.Is(err, ErrPaperEvaluationDeclarationInvalid) {
		t.Fatal("sample method downgrade accepted", err)
	}
	for name, change := range map[string]func(*PaperEvaluationDeclarationCommand){
		"missing": func(c *PaperEvaluationDeclarationCommand) { c.Plan.SampleDesign = nil },
		"unknown": func(c *PaperEvaluationDeclarationCommand) { c.Plan.SampleDesign.Method = "unknown" },
		"weaker count": func(c *PaperEvaluationDeclarationCommand) {
			c.Plan.SampleDesign.MinimumDisjointOccurrences = c.Plan.MinIndependentDecisions - 1
		},
		"unbounded count":   func(c *PaperEvaluationDeclarationCommand) { c.Plan.SampleDesign.MinimumDisjointOccurrences = 1000001 },
		"missing causality": func(c *PaperEvaluationDeclarationCommand) { c.Plan.FeatureCausalityMethod = "" },
	} {
		t.Run(name, func(t *testing.T) {
			bad := clone(c)
			change(&bad)
			if bad.Validate() == nil {
				t.Fatal("invalid sample design accepted")
			}
		})
	}
	c.Plan.SampleDesign.MinimumDisjointOccurrences++
	if _, err := m.DeclarePaperEvaluation(t.Context(), c, clock); !errors.Is(err, ErrPaperEvaluationDeclarationConflict) {
		t.Fatal("retrospective count change accepted", err)
	}
}

func TestDisjointCountCanFailDesignButCannotEstablishIndependence(t *testing.T) {
	at := time.Date(2026, 10, 21, 14, 0, 0, 0, time.UTC)
	windows := []PaperEvaluationSampleWindow{
		{DecisionID: "one", Start: at, End: at.Add(time.Second), Status: "eligible_observed_window"},
		{DecisionID: "two", Start: at.Add(2 * time.Second), End: at.Add(3 * time.Second), Status: "eligible_observed_window"},
	}
	for _, tc := range []struct {
		minimum int
		closed  bool
		status  string
	}{
		{2, false, "pending"}, {3, false, "pending"}, {2, true, "unavailable"}, {3, true, "failed"},
	} {
		v := PaperEvaluationSampleVariant{Windows: clone(windows)}
		evaluateDisjointSampleVariant(&v, tc.minimum, tc.closed)
		if v.DisjointWindows != 2 || v.Status != tc.status {
			t.Fatal(tc, v)
		}
	}
}
