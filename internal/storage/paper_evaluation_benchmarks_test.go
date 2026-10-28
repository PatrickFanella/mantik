package storage

import (
	"errors"
	"testing"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/papereconomics"
)

func TestBenchmarkPlanIsProspectiveAndCannotDowngrade(t *testing.T) {
	m := NewMemory()
	c, at := declarationFixture(t, m)
	c.Plan.Holdout = &PaperEvaluationHoldoutPlan{Method: PaperHoldoutMethod, SplitAt: c.Plan.WindowStart.Add(30 * time.Minute), EmbargoSeconds: 60, MinimumTrainingOccurrences: 2, MinimumHoldoutOccurrences: 2}
	c.Plan.BenchmarkMethod = papereconomics.BenchmarkMethod
	if c.Validate() == nil {
		t.Fatal("original plan accepted a new benchmark method")
	}
	c.Recipe = PaperBenchmarkEvaluationRecipe
	r, err := m.DeclarePaperEvaluation(t.Context(), c, func() time.Time { return at.Add(time.Second) })
	if err != nil || r.Record.Schema != "mantik-owned-paper-evaluation-declaration-v5" {
		t.Fatal(r, err)
	}
	clock := func() time.Time { panic("retry/conflict sampled clock") }
	if retry, err := m.DeclarePaperEvaluation(t.Context(), c, clock); err != nil || !retry.Duplicate || retry.Record.SHA256 != r.Record.SHA256 {
		t.Fatal(retry, err)
	}
	strategies, err := m.PaperStrategies(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	policies, err := m.AllocationPolicies(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := paperEvaluationDeclarationVersion(c, strategies, policies, r.Record.DeclaredAt, "mantik-owned-paper-evaluation-declaration-v4"); !errors.Is(err, ErrPaperEvaluationDeclarationInvalid) {
		t.Fatal("declaration method downgrade accepted", err)
	}
	c.Plan.BenchmarkMethod = "unknown"
	if c.Validate() == nil {
		t.Fatal("unknown method accepted")
	}
}
