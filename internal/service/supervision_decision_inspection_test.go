package service

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/storage"
)

type unavailableDecisionInspectionStore struct{ *storage.Memory }

func (*unavailableDecisionInspectionStore) PaperDecisions(context.Context, storage.PaperDecisionEvaluator) (storage.PaperDecisionCatalog, error) {
	return storage.PaperDecisionCatalog{}, errors.New("fixture owner unavailable")
}
func (*unavailableDecisionInspectionStore) PaperDecisionSources(context.Context, string, storage.PaperDecisionEvaluator) (storage.PaperDecisionSources, error) {
	return storage.PaperDecisionSources{}, errors.New("fixture source owner unavailable")
}
func (*unavailableDecisionInspectionStore) OwnedAllocations(context.Context, storage.PaperDecisionEvaluator, ...storage.PaperOutcomeEvaluator) (storage.OwnedAllocationCatalog, error) {
	return storage.OwnedAllocationCatalog{}, errors.New("fixture allocation owner unavailable")
}
func (*unavailableDecisionInspectionStore) OwnedAllocationComparison(context.Context, string, storage.PaperDecisionEvaluator, ...storage.PaperOutcomeEvaluator) (storage.OwnedAllocationRecord, error) {
	return storage.OwnedAllocationRecord{}, errors.New("fixture allocation owner unavailable")
}
func (*unavailableDecisionInspectionStore) OwnedAdmissionReviews(context.Context, storage.PaperDecisionEvaluator, storage.PaperOutcomeEvaluator) (storage.OwnedAdmissionReviewCatalog, error) {
	return storage.OwnedAdmissionReviewCatalog{}, errors.New("fixture admission owner unavailable")
}
func (*unavailableDecisionInspectionStore) PaperAttribution(context.Context, storage.PaperDecisionEvaluator, storage.PaperOutcomeEvaluator) (storage.PaperAttributionCatalog, error) {
	return storage.PaperAttributionCatalog{}, errors.New("fixture attribution owner unavailable")
}
func (*unavailableDecisionInspectionStore) PaperOutcomes(context.Context, storage.PaperDecisionEvaluator, storage.PaperOutcomeEvaluator) (storage.PaperOutcomeCatalog, error) {
	return storage.PaperOutcomeCatalog{}, errors.New("fixture outcome owner unavailable")
}
func (*unavailableDecisionInspectionStore) PaperOutcomeSources(context.Context, string, storage.PaperDecisionEvaluator, storage.PaperOutcomeEvaluator) (storage.PaperOutcomeSources, error) {
	return storage.PaperOutcomeSources{}, errors.New("fixture outcome source owner unavailable")
}
func (*unavailableDecisionInspectionStore) PaperEvaluationDeclarations(context.Context) (storage.PaperEvaluationDeclarationCatalog, error) {
	return storage.PaperEvaluationDeclarationCatalog{}, errors.New("fixture evaluation plan owner unavailable")
}
func (*unavailableDecisionInspectionStore) PaperEvaluationAssessments(context.Context, storage.PaperDecisionEvaluator, storage.PaperOutcomeEvaluator) (storage.PaperEvaluationAssessmentCatalog, error) {
	return storage.PaperEvaluationAssessmentCatalog{}, errors.New("fixture evaluation assessment owner unavailable")
}
func (*unavailableDecisionInspectionStore) PaperEvaluationEvidence(context.Context, string, func() time.Time, storage.PaperDecisionEvaluator, storage.PaperOutcomeEvaluator) (storage.PaperEvaluationEvidence, error) {
	return storage.PaperEvaluationEvidence{}, errors.New("fixture evaluation evidence owner unavailable")
}
func TestSupervisionDecisionInspectionOwnerOutageAndAuthentication(t *testing.T) {
	h := Handler(&unavailableDecisionInspectionStore{storage.NewMemory()}, "operator")
	for _, spec := range []struct{ kind, path string }{{"outcome", "/v1/paper-outcomes/one"}, {"outcome_market_evidence", "/v1/paper-outcomes/one/market-evidence"}, {"evaluation_declaration", "/v1/paper-evaluation-declarations/one"}, {"evaluation_evidence", "/v1/paper-evaluation-declarations/one/evidence"}, {"evaluation_assessment", "/v1/paper-evaluation-assessments/one"}, {"decision_explanation", "/v1/paper-decisions/one/explanation"}, {"decision_market_evidence", "/v1/paper-decisions/one/market-evidence"}, {"decision_attribution", "/v1/paper-attribution/one"}, {"allocation_comparison", "/v1/allocation-comparisons/one"}, {"admission_review", "/v1/admission-reviews/one"}} {
		body := toolBody("inspect_record", inspectArguments{Kind: spec.kind, ID: "one"})
		if w := supervisionRequest(h, "POST", "/v1/supervision/calls", body, ""); w.Code != http.StatusUnauthorized {
			t.Fatal(w.Code)
		}
		direct := supervisionRequest(h, "GET", spec.path, "", "operator")
		via := supervisionRequest(h, "POST", "/v1/supervision/calls", body, "operator")
		if direct.Code != 503 || via.Code != direct.Code || via.Body.String() != direct.Body.String() {
			t.Fatal("owner outage rewritten", via.Code, via.Body.String(), direct.Body.String())
		}
		minimal := Handler(&outageStore{Store: storage.NewMemory()}, "operator")
		if w := supervisionRequest(minimal, "POST", "/v1/supervision/calls", body, "operator"); w.Code != 404 {
			t.Fatal("absent owner fabricated evidence", w.Code, w.Body.String())
		}
	}
}
