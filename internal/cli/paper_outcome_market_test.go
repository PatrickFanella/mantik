package cli

import (
	"encoding/json"
	"git.subcult.tv/subculture-collective/mantik/internal/service"
	"git.subcult.tv/subculture-collective/mantik/internal/storage"
	"git.subcult.tv/subculture-collective/mantik/internal/workerplan"
	"net/http"
	"reflect"
	"testing"
)

func assertOutcomeMarketHTTP(t *testing.T, request func(string, string, any, int) []byte, url string, clock *ownedDecisionClock, outcome storage.PaperOutcomeRecord) service.PaperOutcomeMarketEvidence {
	t.Helper()
	path := "/v1/paper-outcomes/" + outcome.Command.ID + "/market-evidence"
	clock.panicRead = true
	var got service.PaperOutcomeMarketEvidence
	if e := json.Unmarshal(request("GET", path, nil, 200), &got); e != nil || got.Schema != "mantik-owned-outcome-market-evidence-v2" || got.OutcomeSHA256 != outcome.SHA256 || got.BasisSHA256 != outcome.BasisSHA256 || got.DecisionSHA256 != outcome.Command.DecisionSHA256 || len(got.Rows) == 0 || got.ContinuousCoverageAvailable || got.ShareQuantitiesAvailable || got.AdmissionAvailable || got.ExecutionAvailable || got.QualificationCredit {
		t.Fatal("actual outcome market evidence", got, e)
	}
	selected := 0
	for _, row := range got.Rows {
		if row.Quote != nil && (row.SessionEvidence == nil || !row.SessionEvidence.Evidence.At.Equal(row.AvailableAt) || row.SessionEvidence.Evidence.Quote.InputID != row.InputID) {
			t.Fatal("missing exact quote session cut", row)
		}
		if row.Quote == nil && row.SessionEvidence != nil {
			t.Fatal("session evidence inferred for unavailable quote", row)
		}
		if row.SelectedForwardQuote {
			selected++
			if row.SessionEvidence == nil || !row.SessionEvidence.Evidence.Quote.RegularSessionEligible || len(row.SessionEvidence.CalendarSources) == 0 || row.SessionEvidence.CalendarSources[0].CaptureSHA256 == "" || row.Quote == nil || row.InputID != outcome.Evaluation.InputID || row.CaptureSHA256 != outcome.Evaluation.CaptureSHA256 || row.Quote.AskCents != outcome.Evaluation.ObservedPriceCents || row.Quote.ShareQuantitiesAvailable || row.Quote.SizeUnit != "provider_native_unverified" {
				t.Fatal("forward quote dimensions", row)
			}
		}
	}
	if selected != 1 {
		t.Fatal("selected forward observation missing", got)
	}
	unsigned := got
	unsigned.SHA256 = ""
	if workerplan.Hash(unsigned) != got.SHA256 {
		t.Fatal("market evidence hash")
	}
	var retry service.PaperOutcomeMarketEvidence
	if e := json.Unmarshal(request("GET", path, nil, 200), &retry); e != nil || !reflect.DeepEqual(retry, got) {
		t.Fatal("market retry changed source", retry, e)
	}
	for _, bad := range []string{path + "?", path + "?at=future", "/v1/paper-outcomes/invalid.id/market-evidence"} {
		request("GET", bad, nil, 422)
	}
	request("GET", path, map[string]any{"shares": 100000}, 422)
	request("GET", "/v1/paper-outcomes/absent-market-evidence/market-evidence", nil, 404)
	unauth, e := http.Get(url + path)
	if e != nil {
		t.Fatal(e)
	}
	unauth.Body.Close()
	if unauth.StatusCode != 401 {
		t.Fatal("unauthenticated market evidence", unauth.StatusCode)
	}
	clock.panicRead = false
	return got
}
