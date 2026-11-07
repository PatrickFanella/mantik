package storage

import (
	"context"
	"errors"
	"testing"

	"git.subcult.tv/subculture-collective/mantik/internal/workerplan"
)

// The memo keeps only results bound to their inputs' hash, never errors, and
// hands out a fresh copy on every hit.
func TestPortfolioMemoReusesOnlyBoundResults(t *testing.T) {
	calls := 0
	bind := true
	fail := false
	memo := memoizePortfolio(func(_ context.Context, in PaperEvaluationEntrySources) (PaperEvaluationPortfolio, error) {
		calls++
		if fail {
			return PaperEvaluationPortfolio{}, errors.New("evaluator failed")
		}
		out := PaperEvaluationPortfolio{Method: "fixture", Variants: []PaperEvaluationPortfolioVariant{{StrategyID: "a"}}}
		if bind {
			out.SourceSHA256 = workerplan.Hash(in)
		}
		return out, nil
	})
	ctx := context.Background()
	in := PaperEvaluationEntrySources{AccountingRecipe: "first"}
	first, err := memo(ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	first.Variants[0].StrategyID = "edited"
	second, err := memo(ctx, in)
	if err != nil || calls != 1 || second.Variants[0].StrategyID != "a" {
		t.Fatal("hit was not a fresh copy of the bound result", calls, second, err)
	}
	if _, err := memo(ctx, PaperEvaluationEntrySources{AccountingRecipe: "second"}); err != nil || calls != 2 {
		t.Fatal("different inputs reused a result", calls, err)
	}
	bind = false
	unbound := PaperEvaluationEntrySources{AccountingRecipe: "unbound"}
	_, _ = memo(ctx, unbound)
	_, _ = memo(ctx, unbound)
	if calls != 4 {
		t.Fatal("unbound result was remembered", calls)
	}
	bind, fail = true, true
	failing := PaperEvaluationEntrySources{AccountingRecipe: "failing"}
	_, _ = memo(ctx, failing)
	fail = false
	if _, err := memo(ctx, failing); err != nil || calls != 6 {
		t.Fatal("error was remembered", calls, err)
	}
}
