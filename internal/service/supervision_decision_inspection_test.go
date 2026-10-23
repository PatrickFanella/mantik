package service

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"git.subcult.tv/subculture-collective/mantik/internal/storage"
)

type unavailableDecisionInspectionStore struct{ *storage.Memory }

func (*unavailableDecisionInspectionStore) PaperDecisions(context.Context, storage.PaperDecisionEvaluator) (storage.PaperDecisionCatalog, error) {
	return storage.PaperDecisionCatalog{}, errors.New("fixture owner unavailable")
}
func (*unavailableDecisionInspectionStore) PaperDecisionSources(context.Context, string, storage.PaperDecisionEvaluator) (storage.PaperDecisionSources, error) {
	return storage.PaperDecisionSources{}, errors.New("fixture source owner unavailable")
}
func TestSupervisionDecisionInspectionOwnerOutageAndAuthentication(t *testing.T) {
	h := Handler(&unavailableDecisionInspectionStore{storage.NewMemory()}, "operator")
	for _, spec := range []struct{ kind, path string }{{"decision_explanation", "/v1/paper-decisions/one/explanation"}, {"decision_market_evidence", "/v1/paper-decisions/one/market-evidence"}} {
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
