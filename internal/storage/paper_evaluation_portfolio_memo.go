package storage

import (
	"context"
	"encoding/json"
	"sync"

	"git.subcult.tv/subculture-collective/mantik/internal/workerplan"
)

// maxPortfolioMemoBytes bounds the encoded results one store remembers.
const maxPortfolioMemoBytes = 32 << 20

// memoizePortfolio remembers the configured evaluator's results by the hash of
// their complete inputs. Callers still rederive those inputs from the actual
// owners on every replay; only the evaluator's deterministic computation over
// identical inputs is reused. A result is retained only when it binds the
// same source hash that ownedPortfolioEvaluation requires, and every hit is a
// fresh decoded copy.
func memoizePortfolio(eval PaperPortfolioEvaluator) PaperPortfolioEvaluator {
	var mu sync.Mutex
	entries := map[string][]byte{}
	order := []string{}
	size := 0
	return func(ctx context.Context, inputs PaperEvaluationEntrySources) (PaperEvaluationPortfolio, error) {
		key := workerplan.Hash(inputs)
		mu.Lock()
		raw, ok := entries[key]
		mu.Unlock()
		if ok {
			var value PaperEvaluationPortfolio
			if json.Unmarshal(raw, &value) == nil {
				return value, ctx.Err()
			}
		}
		value, err := eval(ctx, inputs)
		if err != nil || value.SourceSHA256 != key {
			return value, err
		}
		raw, err = json.Marshal(value)
		if err != nil || len(raw) > maxPortfolioMemoBytes {
			return value, nil
		}
		mu.Lock()
		defer mu.Unlock()
		if _, ok := entries[key]; ok {
			return value, nil
		}
		for size+len(raw) > maxPortfolioMemoBytes {
			size -= len(entries[order[0]])
			delete(entries, order[0])
			order = order[1:]
		}
		entries[key] = raw
		order = append(order, key)
		size += len(raw)
		return value, nil
	}
}
