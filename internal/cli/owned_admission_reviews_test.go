package cli

import (
	"encoding/json"
	"git.subcult.tv/subculture-collective/mantik/internal/storage"
	"net/http"
	"reflect"
	"strings"
	"testing"
)

func ownedAdmissionReviewHTTP(t *testing.T, request func(string, string, any, int) []byte, serverURL string, clock *ownedDecisionClock, comparison storage.OwnedAllocationRecord) {
	t.Helper()
	v := comparison.AdmissionChecks[0]
	c := storage.OwnedAdmissionReviewCommand{ID: "http-admission-review", ReceiverID: comparison.Command.ReceiverID, ComparisonID: comparison.Command.ID, ComparisonSHA256: comparison.SHA256, DecisionID: v.DecisionID, DecisionSHA256: v.DecisionSHA256}
	var receipt storage.OwnedAdmissionReviewReceipt
	if e := json.Unmarshal(request("POST", "/v1/admission-reviews", c, 201), &receipt); e != nil || receipt.Duplicate || receipt.Record.Sequence != 1 || !reflect.DeepEqual(receipt.Record.Readiness, v) || receipt.Record.AdmissionAvailable || receipt.Record.ExecutionAvailable || receipt.Record.QualificationCredit {
		t.Fatal("HTTP review", receipt, e)
	}
	clock.panicRead = true
	var retry storage.OwnedAdmissionReviewReceipt
	if e := json.Unmarshal(request("POST", "/v1/admission-reviews", c, 200), &retry); e != nil || !retry.Duplicate || !reflect.DeepEqual(retry.Record, receipt.Record) {
		t.Fatal("HTTP review exact retry", retry, e)
	}
	var all storage.OwnedAdmissionReviewCatalog
	if e := json.Unmarshal(request("GET", "/v1/admission-reviews", nil, 200), &all); e != nil || len(all.Records) != 1 || !reflect.DeepEqual(all.Records[0], receipt.Record) || !reflect.DeepEqual(all.CurrentIDs, []string{c.ID}) {
		t.Fatal("HTTP review catalog", all, e)
	}
	request("GET", "/v1/admission-reviews/"+c.ID, nil, 200)
	supervisionAllocationInspections(t, request, serverURL, c.ReceiverID, comparison.Command.ID, c.ID, c.DecisionID)
	request("GET", "/v1/admission-reviews/missing", nil, 404)
	raw, _ := json.Marshal(c)
	for _, extra := range []string{`"admission_available":true,`, `"balances":{},`, `"snapshot":{},`, `"effect":{},`, `"at":"2026-01-01T00:00:00Z",`, `"result":"passed",`, `"account_id":"chosen",`, `"id":"duplicate",`} {
		request("POST", "/v1/admission-reviews", strings.Replace(string(raw), `"id":`, extra+`"id":`, 1), 422)
	}
	request("POST", "/v1/admission-reviews", string(raw)+`{}`, 422)
	changed := c
	changed.ComparisonSHA256 = strings.Repeat("f", 64)
	request("POST", "/v1/admission-reviews", changed, 409)
	for _, path := range []string{"/v1/admission-reviews?", "/v1/admission-reviews?at=future", "/v1/admission-reviews/invalid.id"} {
		request("GET", path, nil, 422)
	}
	request("GET", "/v1/admission-reviews", map[string]any{"at": "future"}, 422)
	for _, path := range []string{"/v1/admission-reviews", "/v1/admission-reviews/" + c.ID} {
		resp, e := http.Get(serverURL + path)
		if e != nil {
			t.Fatal(e)
		}
		resp.Body.Close()
		if resp.StatusCode != 401 {
			t.Fatal("review without auth", resp.StatusCode)
		}
	}
	clock.panicRead = false
}
