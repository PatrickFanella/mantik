package cli

import (
	"context"
	"encoding/json"
	"net/http"
	"reflect"
	"testing"

	"git.subcult.tv/subculture-collective/mantik/internal/storage"
	"git.subcult.tv/subculture-collective/mantik/internal/workerplan"
)

func paperAttributionHTTP(t *testing.T, request func(string, string, any, int) []byte, serverURL string, clock *ownedDecisionClock, candidate, abstain storage.PaperDecisionRecord) {
	t.Helper()
	outcomes := []storage.PaperOutcomeRecord{}
	for _, d := range []storage.PaperDecisionRecord{candidate, abstain} {
		command := storage.PaperOutcomeCommand{ID: "attribution-" + d.Command.ID, ReceiverID: d.Command.ReceiverID, DecisionID: d.Command.ID, DecisionSHA256: d.SHA256}
		var receipt storage.PaperOutcomeReceipt
		if err := json.Unmarshal(request("POST", "/v1/paper-outcomes", command, 201), &receipt); err != nil {
			t.Fatal(err)
		}
		outcomes = append(outcomes, receipt.Record)
	}
	if outcomes[0].Evaluation.Status != "waiting_horizon" || outcomes[1].Evaluation.Status != "not_applicable" {
		t.Fatal(outcomes)
	}
	// Attribution replays finalized records and needs no model or fresh clock.
	clock.panicRead = true
	defer func() { clock.panicRead = false }()
	var all storage.PaperAttributionCatalog
	if err := json.Unmarshal(request("GET", "/v1/paper-attribution", nil, 200), &all); err != nil || all.Schema != "mantik-owned-paper-attribution-v2" || len(all.Rows) != 5 || all.ReceiverID != candidate.Command.ReceiverID || all.ExecutionAvailable || all.QualificationCredit || all.Authority != "service_owned_phase_attribution_only" {
		t.Fatal(all, err)
	}
	hash := all.SHA256
	all.SHA256 = ""
	if hash != workerplan.Hash(all) {
		t.Fatal("catalogue hash mismatch")
	}
	var row storage.PaperAttributionRow
	if err := json.Unmarshal(request("GET", "/v1/paper-attribution/"+candidate.Command.ID, nil, 200), &row); err != nil || !reflect.DeepEqual(row.Decision, candidate) || len(row.Observations) != 1 || !reflect.DeepEqual(row.Observations[0], outcomes[0]) || len(row.Allocations) != 1 || row.Allocations[0].Status != "defer" || row.Allocations[0].CostCeiling == nil || row.Allocations[0].AdmissionReadiness == nil {
		t.Fatal(row, err)
	}
	var comparisons storage.OwnedAllocationCatalog
	if err := json.Unmarshal(request("GET", "/v1/allocation-comparisons", nil, 200), &comparisons); err != nil || len(comparisons.Records) != 1 || len(comparisons.Records[0].AdmissionChecks) != 1 || !reflect.DeepEqual(*row.Allocations[0].AdmissionReadiness, comparisons.Records[0].AdmissionChecks[0]) {
		t.Fatal("attribution changed retained readiness", comparisons, err)
	}
	if len(comparisons.Records[0].ExecutionSafety) != 1 || row.Allocations[0].ExecutionSafety == nil || !reflect.DeepEqual(*row.Allocations[0].ExecutionSafety, comparisons.Records[0].ExecutionSafety[0]) {
		t.Fatal("HTTP safety binding changed", row.Allocations[0])
	}
	for _, mutate := range []func(*storage.OwnedExecutionSafety){
		func(v *storage.OwnedExecutionSafety) { v.CostCeilingSHA256 = workerplan.Hash("foreign") }, func(v *storage.OwnedExecutionSafety) { v.PolicySHA256 = workerplan.Hash("foreign") }, func(v *storage.OwnedExecutionSafety) { v.DefinitionSHA256 = workerplan.Hash("foreign") }, func(v *storage.OwnedExecutionSafety) { v.ExecutionAvailable = true }, func(v *storage.OwnedExecutionSafety) { v.Safety.StopCents = v.EntryLimitCents },
	} {
		raw, _ := json.Marshal(row)
		var bad storage.PaperAttributionRow
		_ = json.Unmarshal(raw, &bad)
		v := bad.Allocations[0].ExecutionSafety
		mutate(v)
		v.SHA256 = ""
		v.SHA256 = workerplan.Hash(*v)
		bad.SHA256 = ""
		bad.SHA256 = workerplan.Hash(bad)
		if validateAttributionRow(bad, candidate.Command.ReceiverID) == nil {
			t.Fatal("portable safety substitution accepted")
		}
	}
	var reviews storage.OwnedAdmissionReviewCatalog
	if err := json.Unmarshal(request("GET", "/v1/admission-reviews", nil, 200), &reviews); err != nil || len(reviews.Records) != 0 || row.AdmissionReviewHistory == nil || !reflect.DeepEqual(row.AdmissionReviewHistory.Records, reviews.Records) || row.AdmissionReviewHistory.CurrentID != "" || row.AdmissionReviewHistory.SourceSHA256 != workerplan.Hash(reviews) {
		t.Fatal("HTTP lost actual review history", reviews, row.AdmissionReviewHistory, err)
	}
	c := row.Allocations[0].EvaluationContext
	if c == nil || c.SourceSHA256 != comparisons.Records[0].EvaluationContext.SourceSHA256 || !reflect.DeepEqual(c.Binding, comparisons.Records[0].EvaluationContext.Decisions[0]) || c.AdmissionAvailable || c.ExecutionAvailable || c.QualificationCredit {
		t.Fatal("HTTP evaluation context changed owner record", c)
	}
	if row.Economics.ExecutedQuantity != nil || row.Economics.FeesCents != nil || row.Economics.RealizedPnLCents != nil || row.QualificationCredit || row.Economics.Status != "owned_execution_unavailable" || row.Economics.BaselineStatus != "economic_baselines_unavailable" {
		t.Fatal("invented economics", row)
	}
	hash = row.SHA256
	row.SHA256 = ""
	if hash != workerplan.Hash(row) {
		t.Fatal("row hash mismatch")
	}
	var noAction storage.PaperAttributionRow
	if err := json.Unmarshal(request("GET", "/v1/paper-attribution/"+abstain.Command.ID, nil, 200), &noAction); err != nil || !reflect.DeepEqual(noAction.Decision, abstain) || !reflect.DeepEqual(noAction.Observations[0], outcomes[1]) || len(noAction.Allocations) != 0 {
		t.Fatal(noAction, err)
	}
	if noAction.AdmissionReviewHistory == nil || len(noAction.AdmissionReviewHistory.Records) != 0 || noAction.AdmissionReviewHistory.CurrentID != "" {
		t.Fatal("abstention gained admission review", noAction)
	}
	row.SHA256 = hash
	all.SHA256 = workerplan.Hash(all)
	for _, actual := range []storage.PaperAttributionRow{row, noAction} {
		inspected, err := fetchDecisionInspection(context.Background(), serverURL, actual.Decision, "a")
		if err != nil || !reflect.DeepEqual(inspected, actual) {
			t.Fatal("interactive attribution differs from actual owner", inspected, err)
		}
	}
	paperAttributionCLIContract(t, serverURL, all, row, noAction)
	request("GET", "/v1/paper-attribution/missing", nil, 404)
	for _, path := range []string{"/v1/paper-attribution?", "/v1/paper-attribution?account_id=caller", "/v1/paper-attribution/" + candidate.Command.ID + "?model=unavailable", "/v1/paper-attribution/" + candidate.Command.ID + "?at=future", "/v1/paper-attribution/invalid.id"} {
		request("GET", path, nil, 422)
	}
	request("POST", "/v1/paper-attribution", map[string]any{"balance_cents": 123, "realized_pnl_cents": 999}, 405)
	for _, path := range []string{"/v1/paper-attribution", "/v1/paper-attribution/" + candidate.Command.ID} {
		r, err := http.Get(serverURL + path)
		if err != nil {
			t.Fatal(err)
		}
		r.Body.Close()
		if r.StatusCode != 401 {
			t.Fatal(r.StatusCode)
		}
	}
}

func assertRestartedPaperAttribution(t *testing.T, serverURL string) {
	t.Helper()
	req, _ := http.NewRequest("GET", serverURL+"/v1/paper-attribution", nil)
	req.Header.Set("Authorization", "Bearer restart-token")
	response, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	var got storage.PaperAttributionCatalog
	if err = json.NewDecoder(response.Body).Decode(&got); err != nil || response.StatusCode != 200 || len(got.Rows) != 5 || got.ExecutionAvailable || got.QualificationCredit {
		t.Fatal(got, err, response.StatusCode)
	}
	declarationRequest, _ := http.NewRequest("GET", serverURL+"/v1/paper-evaluation-declarations", nil)
	declarationRequest.Header.Set("Authorization", "Bearer restart-token")
	declarationResponse, err := http.DefaultClient.Do(declarationRequest)
	if err != nil {
		t.Fatal(err)
	}
	defer declarationResponse.Body.Close()
	var declarations storage.PaperEvaluationDeclarationCatalog
	if err = json.NewDecoder(declarationResponse.Body).Decode(&declarations); err != nil || declarationResponse.StatusCode != 200 || len(declarations.Records) != 1 || declarations.Records[0].Status != "declared_not_evaluated" || declarations.Records[0].AdmissionAvailable || declarations.Records[0].QualificationCredit {
		t.Fatal("restart changed evaluation declaration", declarations, err)
	}
	count := 0
	for _, row := range got.Rows {
		if row.AdmissionReviewHistory == nil || validateAttributionRow(row, got.ReceiverID) != nil {
			t.Fatal("restart lost valid review history", row)
		}
		count += len(row.AdmissionReviewHistory.Records)
		if row.Economics.FeesCents != nil || row.Economics.RealizedPnLCents != nil {
			t.Fatal("restart invented economic data", row)
		}
	}
	if count != 1 {
		t.Fatal("restart lost retained admission review", count)
	}
}
