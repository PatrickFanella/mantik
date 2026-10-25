package cli

import (
	"context"
	"encoding/json"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/allocation"
	"git.subcult.tv/subculture-collective/mantik/internal/paperstrategy"
	"git.subcult.tv/subculture-collective/mantik/internal/storage"
	"git.subcult.tv/subculture-collective/mantik/internal/workerplan"
)

func paperEvaluationDeclarationHTTP(t *testing.T, request func(string, string, any, int) []byte, serverURL string, clock *ownedDecisionClock, strategy paperstrategy.Record, policy allocation.PolicyRecord) {
	t.Helper()
	model := *policy.Command.ExecutionModel
	model.FeeBPS++
	c := storage.PaperEvaluationDeclarationCommand{ID: "http-paper-evaluation", ReceiverID: strategy.Command.ReceiverID, Recipe: storage.PaperEvaluationRecipe, PolicyID: policy.Command.Policy.ID, PolicySHA256: policy.SHA256, Plan: storage.PaperEvaluationPlan{FamilyID: "http-native-family", Variants: []storage.PaperEvaluationVariant{{ID: strategy.Command.Definition.ID, SHA256: strategy.SHA256}}, WindowStart: clock.at.Add(time.Hour), WindowEnd: clock.at.Add(2 * time.Hour), OutcomeDeadline: clock.at.Add(26 * time.Hour), MinIndependentDecisions: 30, MinimumNetExcessCents: 0, MaximumDrawdownCents: 1000, AdmissionTTLSeconds: 3600, Benchmarks: []string{"cash_no_action", "passive_same_symbol"}, StressModel: model}}
	var receipt storage.PaperEvaluationDeclarationReceipt
	if err := json.Unmarshal(request("POST", "/v1/paper-evaluation-declarations", c, 201), &receipt); err != nil {
		t.Fatal(err)
	}
	r := receipt.Record
	if r.Command.Plan.FamilyID != c.Plan.FamilyID || r.SourceSHA256 == "" || r.Status != "declared_not_evaluated" || len(r.PlannedChecks) != 8 || r.PlannedVariants != 1 || r.EvaluationAvailable || r.AdmissionAvailable || r.ExecutionAvailable || r.QualificationCredit {
		t.Fatal(r)
	}
	hash := r.SHA256
	r.SHA256 = ""
	if workerplan.Hash(r) != hash {
		t.Fatal("hash mismatch")
	}
	r.SHA256 = hash
	clock.panicRead = true
	var duplicate storage.PaperEvaluationDeclarationReceipt
	if err := json.Unmarshal(request("POST", "/v1/paper-evaluation-declarations", c, 200), &duplicate); err != nil || !duplicate.Duplicate || !reflect.DeepEqual(duplicate.Record, r) {
		t.Fatal(duplicate, err)
	}
	var selected storage.PaperEvaluationDeclaration
	if err := json.Unmarshal(request("GET", "/v1/paper-evaluation-declarations/"+c.ID, nil, 200), &selected); err != nil || !reflect.DeepEqual(selected, r) {
		t.Fatal(selected, err)
	}
	var all storage.PaperEvaluationDeclarationCatalog
	if err := json.Unmarshal(request("GET", "/v1/paper-evaluation-declarations", nil, 200), &all); err != nil || len(all.Records) != 1 || !reflect.DeepEqual(all.Records[0], r) {
		t.Fatal(all, err)
	}
	clock.panicRead = false
	var evidence storage.PaperEvaluationEvidence
	path := "/v1/paper-evaluation-declarations/" + c.ID + "/evidence"
	if err := json.Unmarshal(request("GET", path, nil, 200), &evidence); err != nil || evidence.Declaration.SHA256 != r.SHA256 || evidence.Phase != "awaiting_window" || len(evidence.Rows) != 5 || evidence.DistinctProviderOccurrences != 0 || evidence.IndependentSamplesAvailable || evidence.EvaluationAvailable || evidence.AdmissionAvailable || evidence.ExecutionAvailable || evidence.QualificationCredit || !evidence.AsOf.Equal(clock.at) {
		t.Fatal(evidence, err)
	}
	if evidence.Schema != "mantik-owned-paper-evaluation-evidence-v3" || len(evidence.Variants) != 1 {
		t.Fatal(evidence)
	}
	bound := evidence.Variants[0].SampleBound
	if bound.RequiredIndependentDecisions != 30 || bound.ObservedOccurrenceUpperBound != 0 || bound.Status != "pending_outcome_deadline" || bound.IndependenceEstablished || bound.AdmissionAvailable || bound.QualificationCredit {
		t.Fatal("premature sample verdict", bound)
	}
	unsigned := evidence
	unsigned.SHA256 = ""
	if workerplan.Hash(unsigned) != evidence.SHA256 {
		t.Fatal("evidence hash")
	}
	clock.panicRead = true
	request("GET", path+"?at=future", nil, 422)
	request("GET", path, map[string]any{"admission_available": true}, 422)
	request("GET", "/v1/paper-evaluation-declarations/invalid.id/evidence", nil, 422)
	clock.panicRead = false
	request("GET", "/v1/paper-evaluation-declarations/missing/evidence", nil, 404)
	request("POST", path, nil, 405)
	response, err := http.Get(serverURL + path)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != 401 {
		t.Fatal(response.StatusCode)
	}
	// Retention uses the real HTTP owner path; assertions and balances never enter
	// this command. The immutable pending receipt is separate from the live read.
	assessmentCommand := storage.PaperEvaluationAssessmentCommand{ID: "http-assessment", ReceiverID: c.ReceiverID, DeclarationID: c.ID, DeclarationSHA256: receipt.Record.SHA256}
	var assessment storage.PaperEvaluationAssessmentReceipt
	if err := json.Unmarshal(request("POST", "/v1/paper-evaluation-assessments", assessmentCommand, 201), &assessment); err != nil || assessment.Duplicate || assessment.Record.Status != "pending" || assessment.Record.AdmissionAvailable || assessment.Record.ExecutionAvailable || assessment.Record.QualificationCredit || assessment.Record.EvaluationComplete || !reflect.DeepEqual(assessment.Record.Evidence, evidence) {
		t.Fatal(assessment, err)
	}
	if assessment.Record.Schema != "mantik-owned-paper-evaluation-assessment-v4" || assessment.Record.Recipe != "native-predeclared-necessary-checks-v4" || assessment.Record.WarmUp == nil || assessment.Record.WarmUp.Status != "not_required" || assessment.Record.WarmUp.RequiredLookbackObservations != 0 || len(assessment.Record.WarmUp.Strategies) != 1 || !reflect.DeepEqual(assessment.Record.WarmUp.Strategies[0], strategy) {
		t.Fatal("HTTP warm-up source binding", assessment)
	}
	calibration := assessment.Record.ForecastCalibration
	if calibration == nil || calibration.Method != "native-threshold-no-probability-forecast-v1" || calibration.OutputKind != "boolean_threshold_signal" || calibration.Status != "not_applicable" || len(calibration.Strategies) != 1 || calibration.Strategies[0].ID != strategy.Command.Definition.ID || calibration.Strategies[0].SHA256 != strategy.SHA256 {
		t.Fatal("HTTP calibration source binding", assessment)
	}
	foundCalibration := false
	foundWarmup := false
	for _, check := range assessment.Record.Checks {
		if check.Name == "forecast_calibration" {
			foundCalibration = true
			if check.Status != "not_applicable" || check.ReasonCode != calibration.ReasonCode {
				t.Fatal(check)
			}
		}
		if check.Name == "warm_up" {
			foundWarmup = true
			if check.Status != "not_applicable" {
				t.Fatal(check)
			}
		}
	}
	if !foundWarmup || !foundCalibration {
		t.Fatal("HTTP warm-up check missing")
	}
	clock.panicRead = true
	var retried storage.PaperEvaluationAssessmentReceipt
	if err := json.Unmarshal(request("POST", "/v1/paper-evaluation-assessments", assessmentCommand, 200), &retried); err != nil || !retried.Duplicate || !reflect.DeepEqual(retried.Record, assessment.Record) {
		t.Fatal(retried, err)
	}
	var assessments storage.PaperEvaluationAssessmentCatalog
	if err := json.Unmarshal(request("GET", "/v1/paper-evaluation-assessments", nil, 200), &assessments); err != nil || len(assessments.Records) != 1 || !reflect.DeepEqual(assessments.Records[0], assessment.Record) {
		t.Fatal(assessments, err)
	}
	request("GET", "/v1/paper-evaluation-assessments/"+assessmentCommand.ID, nil, 200)
	request("GET", "/v1/paper-evaluation-assessments/missing", nil, 404)
	for _, path := range []string{"/v1/paper-evaluation-assessments?", "/v1/paper-evaluation-assessments?at=future", "/v1/paper-evaluation-assessments/invalid.id"} {
		request("GET", path, nil, 422)
	}
	request("GET", "/v1/paper-evaluation-assessments", map[string]any{"at": "future"}, 422)
	assessmentRaw, _ := json.Marshal(assessmentCommand)
	for _, extra := range []string{`"forecast_calibration":{"status":"passed"},`, `"warm_up":{"status":"qualified"},`, `"strategies":[],`, `"checks":[],`, `"evidence":{},`, `"admission_available":true,`, `"balances":{},`, `"snapshot":{},`, `"effect":{},`, `"assessed_at":"2020-01-01T00:00:00Z",`} {
		request("POST", "/v1/paper-evaluation-assessments", strings.Replace(string(assessmentRaw), `"id":`, extra+`"id":`, 1), 422)
	}
	request("POST", "/v1/paper-evaluation-assessments", strings.Replace(string(assessmentRaw), `"id":`, `"id":"duplicate-field","id":`, 1), 422)
	for _, path := range []string{"/v1/paper-evaluation-assessments", "/v1/paper-evaluation-assessments/" + assessmentCommand.ID} {
		response, e := http.Get(serverURL + path)
		if e != nil {
			t.Fatal(e)
		}
		response.Body.Close()
		if response.StatusCode != 401 {
			t.Fatal(response.StatusCode)
		}
	}
	var guardedComparisons storage.OwnedAllocationCatalog
	if err := json.Unmarshal(request("GET", "/v1/allocation-comparisons", nil, 200), &guardedComparisons); err != nil || len(guardedComparisons.Records) == 0 {
		t.Fatal(guardedComparisons, err)
	}
	var guardedRetry storage.OwnedAllocationReceipt
	if err := json.Unmarshal(request("POST", "/v1/allocation-comparisons", guardedComparisons.Records[0].Command, 200), &guardedRetry); err != nil || !guardedRetry.Duplicate || !reflect.DeepEqual(guardedRetry.Record, guardedComparisons.Records[0]) {
		t.Fatal("assessment-owner HTTP retry", guardedRetry, err)
	}
	request("GET", "/v1/paper-attribution", nil, 200)
	clock.panicRead = false
	clock.at = assessment.Record.AssessedAt.Add(time.Nanosecond)
	newComparison := guardedComparisons.Records[0].Command
	newComparison.ID = "http-assessment-bound-comparison"
	var boundComparison storage.OwnedAllocationReceipt
	if e := json.Unmarshal(request("POST", "/v1/allocation-comparisons", newComparison, 201), &boundComparison); e != nil || boundComparison.Record.Schema != "mantik-owned-allocation-deferral-v12" || boundComparison.Record.EvaluationContext.Schema != "mantik-owned-allocation-evaluation-context-v2" || len(boundComparison.Record.EvaluationContext.Decisions[0].Assessments) != 1 || boundComparison.Record.EvaluationContext.Decisions[0].Assessments[0].SHA256 != assessment.Record.SHA256 {
		t.Fatal("HTTP assessment context", boundComparison, e)
	}
	readiness := boundComparison.Record.AdmissionChecks[0]
	expectedDecisionStatus := "decision_not_retained"
	for _, row := range assessment.Record.Evidence.Rows {
		if row.Decision.Command.ID == newComparison.Decisions[0].ID && row.Decision.SHA256 == newComparison.Decisions[0].SHA256 {
			expectedDecisionStatus = row.Status
			break
		}
	}
	if readiness.Schema != "mantik-owned-admission-readiness-v5" || readiness.Binding == nil || readiness.Binding.EvaluationBindingSHA256 != workerplan.Hash(boundComparison.Record.EvaluationContext.Decisions[0]) || len(readiness.EvaluationEvidence) != 1 || readiness.EvaluationEvidence[0].AssessmentSHA256 != assessment.Record.SHA256 || readiness.EvaluationEvidence[0].DecisionEvidenceStatus != expectedDecisionStatus || readiness.AdmissionAvailable || readiness.ExecutionAvailable || readiness.QualificationCredit {
		t.Fatal("HTTP exact assessment evidence readiness", readiness)
	}
	ownedAdmissionReviewHTTP(t, request, serverURL, clock, boundComparison.Record)
	var attribution storage.PaperAttributionCatalog
	if e := json.Unmarshal(request("GET", "/v1/paper-attribution", nil, 200), &attribution); e != nil {
		t.Fatal(e)
	}
	for _, row := range attribution.Rows {
		if row.AdmissionReviewHistory != nil && len(row.AdmissionReviewHistory.Records) > 0 {
			assertAttributionReviewPresentation(t, c.ReceiverID, row)
			inspected, err := fetchDecisionInspection(context.Background(), serverURL, row.Decision, "a")
			if err != nil || !reflect.DeepEqual(inspected, row) {
				t.Fatal("interactive attribution lost retained admission review", inspected, err)
			}
			var out, diag strings.Builder
			if err := Run(context.Background(), []string{"paper-attribution", "--api-url", serverURL, "--receiver-id", c.ReceiverID, "--id", row.Decision.Command.ID, "--record-sha256", row.SHA256}, &out, &diag); err != nil || !strings.Contains(out.String(), "ADMISSION_REVIEW \"http-admission-review\" current=true") || !strings.Contains(out.String(), "admission=false execution=false qualification=false") {
				t.Fatal("actual review CLI presentation", err, out.String(), diag.String())
			}
		}
		if e := validateAttributionRow(row, c.ReceiverID); e != nil {
			t.Fatal("CLI rejected actual assessment attribution", e)
		}
	}

	raw, _ := json.Marshal(c)
	for _, extra := range []string{`"admission_available":true,`, `"balances":{},`, `"evaluation_results":[],`, `"at":"2020-01-01T00:00:00Z",`, `"qualification_credit":true,`, `"effect":{},`} {
		request("POST", "/v1/paper-evaluation-declarations", strings.Replace(string(raw), `"id":`, extra+`"id":`, 1), 422)
	}
	bad := c
	bad.ID = "same-family-new-id"
	request("POST", "/v1/paper-evaluation-declarations", bad, 409)
	bad = c
	bad.ID = "late-window"
	bad.Plan.FamilyID = "late-family"
	bad.Plan.WindowStart = clock.at
	request("POST", "/v1/paper-evaluation-declarations", bad, 422)
	for _, path := range []string{"/v1/paper-evaluation-declarations?", "/v1/paper-evaluation-declarations?at=future", "/v1/paper-evaluation-declarations/" + c.ID + "?model=unavailable", "/v1/paper-evaluation-declarations/invalid.id"} {
		request("GET", path, nil, 422)
	}
	request("GET", "/v1/paper-evaluation-declarations/missing", nil, 404)
	for _, path := range []string{"/v1/paper-evaluation-declarations", "/v1/paper-evaluation-declarations/" + c.ID} {
		response, err := http.Get(serverURL + path)
		if err != nil {
			t.Fatal(err)
		}
		response.Body.Close()
		if response.StatusCode != 401 {
			t.Fatal(response.StatusCode)
		}
	}
}
