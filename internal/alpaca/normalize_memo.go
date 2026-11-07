package alpaca

import (
	"slices"
	"sync"
)

// maxNormalizedMemoBytes bounds the capture encodings remembered as keys.
const maxNormalizedMemoBytes = 16 << 20

type normalizeMemo struct {
	mu      sync.Mutex
	entries map[string]Result
	size    int
}

var normalized = &normalizeMemo{entries: map[string]Result{}}

func (m *normalizeMemo) get(key string) (Result, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out, ok := m.entries[key]
	if !ok {
		return Result{}, false
	}
	return copyResult(out), true
}

// put forgets everything when the budget would be exceeded; replay then
// recomputes, so eviction only costs time.
func (m *normalizeMemo) put(key string, out Result) {
	if len(key) > maxNormalizedMemoBytes {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.entries[key]; ok {
		return
	}
	if m.size+len(key) > maxNormalizedMemoBytes {
		m.entries, m.size = map[string]Result{}, 0
	}
	m.entries[key] = copyResult(out)
	m.size += len(key)
}

// copyResult detaches every slice and pointer a caller could mutate. Results
// with corporate actions are never remembered.
func copyResult(r Result) Result {
	out := r
	out.Inputs = slices.Clone(r.Inputs)
	for i, input := range out.Inputs {
		if input.Provenance != nil {
			p := *input.Provenance
			out.Inputs[i].Provenance = &p
		}
	}
	out.Coverage = slices.Clone(r.Coverage)
	out.Sessions = slices.Clone(r.Sessions)
	out.Unsupported = slices.Clone(r.Unsupported)
	return out
}
