package service

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"

	"git.subcult.tv/subculture-collective/mantik/internal/storage"
)

// legacyAttributionStore has sealed history without its segment index: the
// targeted reader refuses, and the catalogue still answers.
type legacyAttributionStore struct {
	*storage.Memory
	targeted  error
	catalogue storage.PaperAttributionCatalog
	reads     int
}

func (s *legacyAttributionStore) PaperAttributionDecision(context.Context, string, storage.PaperDecisionEvaluator, storage.PaperOutcomeEvaluator) (storage.PaperAttributionRow, error) {
	return storage.PaperAttributionRow{}, s.targeted
}
func (s *legacyAttributionStore) PaperAttribution(context.Context, storage.PaperDecisionEvaluator, storage.PaperOutcomeEvaluator) (storage.PaperAttributionCatalog, error) {
	s.reads++
	return s.catalogue, nil
}

func TestPaperAttributionTargetFallsBackOnlyForUnindexedHistory(t *testing.T) {
	row := storage.PaperAttributionRow{Decision: storage.PaperDecisionRecord{Command: storage.PaperDecisionCommand{ID: "one"}}, SHA256: "fixture-row"}
	store := &legacyAttributionStore{Memory: storage.NewMemory(), targeted: storage.ErrPaperAttributionIndexUnavailable, catalogue: storage.PaperAttributionCatalog{Rows: []storage.PaperAttributionRow{row}}}
	h := Handler(store, "operator")
	w := supervisionRequest(h, "GET", "/v1/paper-attribution/one", "", "operator")
	var got storage.PaperAttributionRow
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &got) != nil || !reflect.DeepEqual(got, row) || store.reads != 1 {
		t.Fatal("unindexed history did not fall back to the catalogue row", w.Code, w.Body.String())
	}
	if w := supervisionRequest(h, "GET", "/v1/paper-attribution/two", "", "operator"); w.Code != 404 || store.reads != 2 {
		t.Fatal("catalogue fallback invented a row", w.Code)
	}
	// Any other targeted failure is final: no second, unbounded read.
	for err, code := range map[error]int{storage.ErrPaperAttributionNotFound: 404, errors.New("owner unavailable"): 503} {
		store.targeted, store.reads = err, 0
		if w := supervisionRequest(h, "GET", "/v1/paper-attribution/one", "", "operator"); w.Code != code || store.reads != 0 {
			t.Fatal("targeted failure fell back to the catalogue", err, w.Code, store.reads)
		}
	}
}
