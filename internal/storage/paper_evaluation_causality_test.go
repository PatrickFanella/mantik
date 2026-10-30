package storage

import (
	"errors"
	"reflect"
	"testing"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/papereconomics"
)

func TestFeatureCausalityRequiresProspectiveVersionedPlan(t *testing.T) {
	m := NewMemory()
	c, at := declarationFixture(t, m)
	c.Plan.Holdout = &PaperEvaluationHoldoutPlan{Method: PaperHoldoutMethod, SplitAt: c.Plan.WindowStart.Add(30 * time.Minute), EmbargoSeconds: 60, MinimumTrainingOccurrences: 2, MinimumHoldoutOccurrences: 2}
	c.Plan.BenchmarkMethod = papereconomics.BenchmarkMethod
	c.Recipe = PaperBenchmarkEvaluationRecipe
	legacy, err := m.DeclarePaperEvaluation(t.Context(), c, func() time.Time { return at.Add(time.Second) })
	if err != nil {
		t.Fatal(err)
	}
	c.ID = "causal-plan"
	c.Plan.FamilyID = "causal-family"
	c.Plan.FeatureCausalityMethod = PaperFeatureCausalityMethod
	if c.Validate() == nil {
		t.Fatal("legacy recipe accepted new check")
	}
	c.Recipe = PaperCausalEvaluationRecipe
	got, err := m.DeclarePaperEvaluation(t.Context(), c, func() time.Time { return at.Add(2 * time.Second) })
	if err != nil || got.Record.Schema != "mantik-owned-paper-evaluation-declaration-v6" {
		t.Fatal(got, err)
	}
	clock := func() time.Time { panic("retry or conflict sampled clock") }
	if retry, err := m.DeclarePaperEvaluation(t.Context(), c, clock); err != nil || !retry.Duplicate || !reflect.DeepEqual(retry.Record, got.Record) {
		t.Fatal(retry, err)
	}
	all, err := m.PaperEvaluationDeclarations(t.Context())
	if err != nil || len(all.Records) != 2 {
		t.Fatal("new method changed legacy declaration", all, err)
	}
	legacyPreserved := false
	for _, record := range all.Records {
		if record.Command.ID == legacy.Record.Command.ID {
			legacyPreserved = reflect.DeepEqual(record, legacy.Record)
		}
	}
	if !legacyPreserved {
		t.Fatal("legacy declaration was rewritten")
	}
	strategies, _ := m.PaperStrategies(t.Context())
	policies, _ := m.AllocationPolicies(t.Context())
	if _, err := paperEvaluationDeclarationVersion(c, strategies, policies, got.Record.DeclaredAt, "mantik-owned-paper-evaluation-declaration-v5"); !errors.Is(err, ErrPaperEvaluationDeclarationInvalid) {
		t.Fatal("causality declaration downgrade accepted", err)
	}
	for name, change := range map[string]func(*PaperEvaluationDeclarationCommand){
		"unknown method":     func(c *PaperEvaluationDeclarationCommand) { c.Plan.FeatureCausalityMethod = "unknown" },
		"missing method":     func(c *PaperEvaluationDeclarationCommand) { c.Plan.FeatureCausalityMethod = "" },
		"missing benchmarks": func(c *PaperEvaluationDeclarationCommand) { c.Plan.BenchmarkMethod = "" },
		"missing holdout":    func(c *PaperEvaluationDeclarationCommand) { c.Plan.Holdout = nil },
	} {
		t.Run(name, func(t *testing.T) {
			bad := clone(c)
			change(&bad)
			if bad.Validate() == nil {
				t.Fatal("inapplicable plan accepted")
			}
		})
	}
	c.Plan.FeatureCausalityMethod = ""
	c.Recipe = PaperBenchmarkEvaluationRecipe
	if _, err := m.DeclarePaperEvaluation(t.Context(), c, clock); !errors.Is(err, ErrPaperEvaluationDeclarationConflict) {
		t.Fatal("retrospective method removal accepted", err)
	}
}

func TestNativeCausalityCannotEraseKnownLeakageOrUseExcludedRows(t *testing.T) {
	r := PaperEvaluationAssessment{Status: "pending", Checks: []PaperEvaluationCheck{
		{StrategyID: "unsafe", Name: "leakage", Status: "unavailable"},
		{StrategyID: "empty", Name: "leakage", Status: "unavailable"},
		{StrategyID: "safe", Name: "leakage", Status: "unavailable"},
	}, Evidence: PaperEvaluationEvidence{Rows: []PaperEvaluationEvidenceRow{}}}
	for _, row := range []struct{ id, status string }{{"unsafe", "selected_research_evidence"}, {"unsafe", "source_unavailable_at_decision"}, {"empty", "synthetic_not_selected"}, {"empty", "abstention"}, {"safe", "selected_research_evidence"}} {
		r.Evidence.Rows = append(r.Evidence.Rows, PaperEvaluationEvidenceRow{Status: row.status, Decision: PaperDecisionRecord{Command: PaperDecisionCommand{StrategyID: row.id}}})
	}
	applyKnownSourceLeakage(&r)
	applyNativeFeatureCausality(&r)
	if r.Status != "blocked" || r.Checks[0].Status != "failed" || r.Checks[1].Status != "unavailable" || r.Checks[2].Status != "passed" || r.EvaluationComplete || r.AdmissionAvailable || r.ExecutionAvailable || r.QualificationCredit {
		t.Fatal(r)
	}
}
