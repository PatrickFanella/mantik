package cli

import (
	"encoding/json"
	"net/http"
	"reflect"
	"strings"
	"testing"

	"git.subcult.tv/subculture-collective/mantik/internal/storage"
)

func decisionReviewHTTP(t *testing.T, request func(string, string, any, int) []byte, serverURL string, clock *ownedDecisionClock, d storage.PaperDecisionRecord) {
	t.Helper()
	c := storage.DecisionReviewCommand{ID: "consistent-review", ReceiverID: d.Command.ReceiverID, DecisionID: d.Command.ID, DecisionSHA256: d.SHA256, Explanation: "This opaque explanation includes an unsupported profitability promise.", Claims: []storage.DecisionReviewClaim{{Field: "action", Value: d.Evaluation.Action}, {Field: "reference_price_cents", Value: "10000"}}}
	var consistent storage.DecisionReviewReceipt
	if err := json.Unmarshal(request("POST", "/v1/paper-decision-reviews", c, 201), &consistent); err != nil || consistent.Record.Status != "claims_consistent" || consistent.Record.ProseVerified || consistent.Record.ModelUsed || consistent.Record.AdmissionAvailable || consistent.Record.ExecutionAvailable || consistent.Record.QualificationCredit || consistent.Record.Command.Explanation != c.Explanation {
		t.Fatal(consistent, err)
	}
	c.ID = "contradictory-review"
	c.Claims = []storage.DecisionReviewClaim{{Field: "action", Value: "exit"}, {Field: "realized_pnl_cents", Value: "100000"}}
	c.Explanation = "Original contradiction\n<untrusted>realized profit</untrusted>"
	var result storage.DecisionReviewReceipt
	if err := json.Unmarshal(request("POST", "/v1/paper-decision-reviews", c, 201), &result); err != nil || result.Record.Status != "review_required" || result.Record.BasisSHA256 != d.BasisSHA256 || result.Record.Findings[0].Status != "contradiction" || result.Record.Findings[1].Status != "unsupported_field" || result.Record.Findings[1].Expected != nil {
		t.Fatal(result, err)
	}
	clock.panicRead = true
	var retry storage.DecisionReviewReceipt
	if err := json.Unmarshal(request("POST", "/v1/paper-decision-reviews", c, 200), &retry); err != nil || !retry.Duplicate || !reflect.DeepEqual(result.Record, retry.Record) {
		t.Fatal(retry, err)
	}
	var record storage.DecisionReviewRecord
	if err := json.Unmarshal(request("GET", "/v1/paper-decision-reviews/"+c.ID, nil, 200), &record); err != nil || !reflect.DeepEqual(record, result.Record) {
		t.Fatal(record, err)
	}
	var reviewCase storage.DecisionReviewCase
	if err := json.Unmarshal(request("GET", "/v1/decision-review-cases/"+result.Record.CaseID, nil, 200), &reviewCase); err != nil || reviewCase.DecisionSHA256 != d.SHA256 || reviewCase.Status != "open" || !reflect.DeepEqual(reviewCase.ReviewIDs, []string{c.ID}) {
		t.Fatal(reviewCase, err)
	}
	clock.panicRead = false
	c.ID = "same-decision-another-review"
	c.Claims = c.Claims[1:]
	request("POST", "/v1/paper-decision-reviews", c, 201)
	var catalog storage.DecisionReviewCatalog
	if err := json.Unmarshal(request("GET", "/v1/paper-decision-reviews", nil, 200), &catalog); err != nil || len(catalog.Records) != 3 || len(catalog.Cases) != 1 || len(catalog.Cases[0].ReviewIDs) != 2 {
		t.Fatal(catalog, err)
	}
	request("GET", "/v1/decision-review-cases", nil, 200)
	request("GET", "/v1/paper-decision-reviews/missing", nil, 404)
	request("GET", "/v1/decision-review-cases/missing", nil, 404)
	for _, path := range []string{"/v1/paper-decision-reviews?", "/v1/decision-review-cases?model=unavailable", "/v1/paper-decision-reviews/" + c.ID + "?at=tomorrow"} {
		request("GET", path, nil, 422)
	}
	request("POST", "/v1/paper-decision-reviews?", c, 422)
	raw, _ := json.Marshal(c)
	for _, field := range []string{`"admission":true,`, `"effect":{},`, `"balance_cents":100,`, `"model":"unavailable",`, `"id":"duplicate",`} {
		request("POST", "/v1/paper-decision-reviews", strings.Replace(string(raw), `"id":`, field+`"id":`, 1), 422)
	}
	request("POST", "/v1/paper-decision-reviews", string(raw)+` {}`, 422)
	bad := c
	bad.ID = "bad-pin"
	bad.DecisionSHA256 = strings.Repeat("a", 64)
	request("POST", "/v1/paper-decision-reviews", bad, 409)
	bad = c
	bad.ID = "missing-decision"
	bad.DecisionID = "missing"
	request("POST", "/v1/paper-decision-reviews", bad, 404)
	bad = c
	bad.Claims = nil
	request("POST", "/v1/paper-decision-reviews", bad, 422)
	bad = c
	bad.Explanation = strings.Repeat("x", storage.MaxDecisionReviewCommandBytes)
	request("POST", "/v1/paper-decision-reviews", bad, 422)
	res, err := http.Get(serverURL + "/v1/decision-review-cases")
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != 401 {
		t.Fatal(res.StatusCode)
	}
	// The original record and deterministic explanation remain untouched.
	var actual storage.PaperDecisionRecord
	if err := json.Unmarshal(request("GET", "/v1/paper-decisions/"+d.Command.ID, nil, 200), &actual); err != nil || !reflect.DeepEqual(actual, d) {
		t.Fatal(actual, err)
	}
}
