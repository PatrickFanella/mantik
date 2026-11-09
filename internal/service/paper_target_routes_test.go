package service

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	"git.subcult.tv/subculture-collective/mantik/internal/storage"
)

// Storage contracts prove archived custody. This adapter proves targeted HTTP
// reads use that source contract even when an active catalogue cannot serve it.
type paperTargetRouteStore struct {
	*storage.Memory
	sourceErr error
}

func (s *paperTargetRouteStore) PaperDecisions(context.Context, storage.PaperDecisionEvaluator) (storage.PaperDecisionCatalog, error) {
	return storage.PaperDecisionCatalog{}, errors.New("active catalogue unavailable in targeted route fixture")
}

func (s *paperTargetRouteStore) PaperOutcomes(context.Context, storage.PaperDecisionEvaluator, storage.PaperOutcomeEvaluator) (storage.PaperOutcomeCatalog, error) {
	return storage.PaperOutcomeCatalog{}, errors.New("active catalogue unavailable in targeted route fixture")
}

func (s *paperTargetRouteStore) PaperDecisionSources(ctx context.Context, id string, de storage.PaperDecisionEvaluator) (storage.PaperDecisionSources, error) {
	if s.sourceErr != nil {
		return storage.PaperDecisionSources{}, s.sourceErr
	}
	return s.Memory.PaperDecisionSources(ctx, id, de)
}

func (s *paperTargetRouteStore) PaperOutcomeSources(ctx context.Context, id string, de storage.PaperDecisionEvaluator, oe storage.PaperOutcomeEvaluator) (storage.PaperOutcomeSources, error) {
	if s.sourceErr != nil {
		return storage.PaperOutcomeSources{}, s.sourceErr
	}
	return s.Memory.PaperOutcomeSources(ctx, id, de, oe)
}

func TestPaperTargetRoutesUseOwnerSources(t *testing.T) {
	f := newQuoteFixture(t)
	c := storage.PaperDecisionCommand{ID: "target", ReceiverID: f.owner, StrategyID: f.definition.ID, StrategySHA256: f.strategy.Record.SHA256, RegistrationID: f.reg.ID}
	d, err := f.store.FinalizePaperDecision(t.Context(), c, f.now, evaluateOwnedPaperDecision)
	if err != nil {
		t.Fatal(err)
	}
	o, err := f.store.AssessPaperOutcome(t.Context(), storage.PaperOutcomeCommand{ID: "target-outcome", ReceiverID: f.owner, DecisionID: c.ID, DecisionSHA256: d.Record.SHA256}, f.now, evaluateOwnedPaperDecision, evaluateOwnedPaperOutcome)
	if err != nil {
		t.Fatal(err)
	}
	s := &paperTargetRouteStore{Memory: f.store}
	mux := http.NewServeMux()
	// Targeted reads must never use a clock.
	paperDecisionRoutes(mux, s, nil)
	paperOutcomeRoutes(mux, s, nil)
	explanation, err := explainPaperDecision(d.Record)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		path string
		want any
	}{
		{"/v1/paper-decisions/target", d.Record},
		{"/v1/paper-outcomes/target-outcome", o.Record},
		{"/v1/paper-decisions/target/explanation", explanation},
	} {
		t.Run(tc.path, func(t *testing.T) {
			r := httptest.NewRecorder()
			mux.ServeHTTP(r, httptest.NewRequest(http.MethodGet, tc.path, nil))
			var got, want any
			raw, err := json.Marshal(tc.want)
			if err != nil {
				t.Fatal(err)
			}
			if err := json.Unmarshal(raw, &want); err != nil {
				t.Fatal(err)
			}
			if err := json.Unmarshal(r.Body.Bytes(), &got); err != nil || r.Code != http.StatusOK || !reflect.DeepEqual(got, want) {
				t.Fatal("targeted HTTP read changed native owner", r.Code, r.Body.String(), err)
			}
			s.sourceErr = storage.ErrPaperRecordArchiveConflict
			failed := httptest.NewRecorder()
			mux.ServeHTTP(failed, httptest.NewRequest(http.MethodGet, tc.path, nil))
			s.sourceErr = nil
			if failed.Code != http.StatusServiceUnavailable {
				t.Fatal("failed original source returned a successful target", failed.Code)
			}
		})
	}
}
