package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"

	"errors"
	"git.subcult.tv/subculture-collective/mantik/internal/allocation"
	"git.subcult.tv/subculture-collective/mantik/internal/paperstrategy"
	"git.subcult.tv/subculture-collective/mantik/internal/service"
	"git.subcult.tv/subculture-collective/mantik/internal/storage"
	"git.subcult.tv/subculture-collective/mantik/internal/workerplan"
	"sync"
)

func paperEvaluationDeclarationHTTP(t *testing.T, request func(string, string, any, int) []byte, serverURL string, clock *ownedDecisionClock, strategy paperstrategy.Record, policy allocation.PolicyRecord, backend service.PaperEvaluationWorkerStore) {
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

	replayPath := "/v1/paper-evaluation-declarations/" + c.ID + "/entry-replay"
	var entryReplay service.PaperEvaluationEntryReplay
	if err := json.Unmarshal(request("GET", replayPath, nil, 200), &entryReplay); err != nil || entryReplay.Evidence.SHA256 != evidence.SHA256 || entryReplay.PolicySHA256 != policy.SHA256 || len(entryReplay.Rows) != 0 || len(entryReplay.Variants) != 1 || entryReplay.Variants[0].Status != "no_selected_decisions" || entryReplay.PortfolioEconomicsAvailable || entryReplay.AdmissionAvailable || entryReplay.ExecutionAvailable || entryReplay.QualificationCredit {
		t.Fatal("HTTP entry replay", entryReplay, err)
	}
	pin := entryReplay.SHA256
	entryReplay.SHA256 = ""
	if workerplan.Hash(entryReplay) != pin {
		t.Fatal("entry replay digest")
	}
	request("GET", replayPath+"?model=caller", nil, 422)
	request("GET", replayPath, map[string]any{"quote": evidence}, 422)
	request("POST", replayPath, nil, 405)
	request("GET", "/v1/paper-evaluation-declarations/missing/entry-replay", nil, 404)
	unauthorized, err := http.Get(serverURL + replayPath)
	if err != nil {
		t.Fatal(err)
	}
	unauthorized.Body.Close()
	if unauthorized.StatusCode != 401 {
		t.Fatal(unauthorized.StatusCode)
	}
	// The current evidence projection samples the service clock; the adapter must
	// preserve that owner's cutoff and complete response without retaining a new
	// assessment or accepting caller evidence/results.
	supervisionOwnedInspections(t, request, serverURL, c.ReceiverID, []supervisionInspection{
		{"evaluation_evidence", c.ID, path},
	})
	clock.panicRead = true
	request("POST", "/v1/supervision/calls", map[string]any{"tool": "inspect_record", "arguments": map[string]any{"kind": "evaluation_evidence", "id": c.ID, "evidence": evidence}}, 422)
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
	beforePlans := request("GET", "/v1/paper-evaluation-declarations", nil, 200)
	beforeAssessments := request("GET", "/v1/paper-evaluation-assessments", nil, 200)
	supervisionOwnedInspections(t, request, serverURL, c.ReceiverID, []supervisionInspection{
		{"evaluation_declaration", c.ID, "/v1/paper-evaluation-declarations/" + c.ID},
		{"evaluation_assessment", assessmentCommand.ID, "/v1/paper-evaluation-assessments/" + assessmentCommand.ID},
	})
	if !bytes.Equal(beforePlans, request("GET", "/v1/paper-evaluation-declarations", nil, 200)) || !bytes.Equal(beforeAssessments, request("GET", "/v1/paper-evaluation-assessments", nil, 200)) {
		t.Fatal("inspection mutated evaluation owners")
	}

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
	if e := json.Unmarshal(request("POST", "/v1/allocation-comparisons", newComparison, 201), &boundComparison); e != nil || boundComparison.Record.Schema != "mantik-owned-allocation-deferral-v14" || boundComparison.Record.EvaluationContext.Schema != "mantik-owned-allocation-evaluation-context-v2" || len(boundComparison.Record.EvaluationContext.Decisions[0].Assessments) != 1 || boundComparison.Record.EvaluationContext.Decisions[0].Assessments[0].SHA256 != assessment.Record.SHA256 {
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
	if readiness.Schema != "mantik-owned-admission-readiness-v6" || readiness.Binding == nil || readiness.Binding.EvaluationBindingSHA256 != workerplan.Hash(boundComparison.Record.EvaluationContext.Decisions[0]) || len(readiness.EvaluationEvidence) != 1 || readiness.EvaluationEvidence[0].AssessmentSHA256 != assessment.Record.SHA256 || readiness.EvaluationEvidence[0].DecisionEvidenceStatus != expectedDecisionStatus || readiness.AdmissionAvailable || readiness.ExecutionAvailable || readiness.QualificationCredit {
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
	automaticPaperEvaluationHTTP(t, request, serverURL, clock, receipt.Record, assessment.Record, backend)

}

type interleavedEvaluationStore struct {
	service.PaperEvaluationWorkerStore
	once       sync.Once
	before     func()
	afterError error
}

func (s *interleavedEvaluationStore) AssessPaperEvaluation(ctx context.Context, c storage.PaperEvaluationAssessmentCommand, now func() time.Time, de storage.PaperDecisionEvaluator, oe storage.PaperOutcomeEvaluator) (storage.PaperEvaluationAssessmentReceipt, error) {
	s.once.Do(s.before)
	receipt, err := s.PaperEvaluationWorkerStore.AssessPaperEvaluation(ctx, c, now, de, oe)
	if err == nil && s.afterError != nil {
		return storage.PaperEvaluationAssessmentReceipt{}, s.afterError
	}
	return receipt, err
}

// Uses the actual service-selected owner and live transaction clock; no imported
// check results, market sizes, financial effects, models or providers are used.
func automaticPaperEvaluationHTTP(t *testing.T, request func(string, string, any, int) []byte, base string, clock *ownedDecisionClock, d storage.PaperEvaluationDeclaration, pending storage.PaperEvaluationAssessment, backend service.PaperEvaluationWorkerStore) {
	t.Helper()
	ctx := context.Background()
	before := request("GET", "/v1/paper-evaluation-assessments", nil, 200)
	clock.at = d.Command.Plan.OutcomeDeadline
	if err := service.RunPaperEvaluationCycle(ctx, backend, clock); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, request("GET", "/v1/paper-evaluation-assessments", nil, 200)) {
		t.Fatal("sealed at inclusive deadline")
	}
	command := storage.PaperEvaluationAssessmentCommand{ID: service.AutomaticPaperEvaluationID(d), ReceiverID: d.Command.ReceiverID, DeclarationID: d.Command.ID, DeclarationSHA256: d.SHA256}
	clock.panicRead = true
	request("POST", "/v1/paper-evaluation-assessments", command, 422)
	clock.panicRead = false
	clock.at = d.Command.Plan.OutcomeDeadline.Add(time.Nanosecond)
	rollback := &interleavedEvaluationStore{PaperEvaluationWorkerStore: backend, before: func() { clock.at = d.Command.Plan.OutcomeDeadline }}
	if err := service.RunPaperEvaluationCycle(ctx, rollback, clock); !errors.Is(err, storage.ErrPaperEvaluationAssessmentInvalid) {
		t.Fatal("rollback occupied automatic identity", err)
	}
	if !bytes.Equal(before, request("GET", "/v1/paper-evaluation-assessments", nil, 200)) {
		t.Fatal("rollback retained a partial assessment")
	}
	clock.at = d.Command.Plan.OutcomeDeadline.Add(time.Nanosecond)
	lostAck := errors.New("fixture acknowledgement lost after actual commit")
	live := &interleavedEvaluationStore{PaperEvaluationWorkerStore: backend, before: func() { clock.at = clock.at.Add(time.Second) }, afterError: lostAck}
	if err := service.RunPaperEvaluationCycle(ctx, live, clock); !errors.Is(err, lostAck) {
		t.Fatal(err)
	}
	var sealed storage.PaperEvaluationAssessment
	if e := json.Unmarshal(request("GET", "/v1/paper-evaluation-assessments/"+command.ID, nil, 200), &sealed); e != nil || !sealed.AssessedAt.Equal(clock.at) || sealed.Status != "blocked" || sealed.Evidence.Phase != "outcome_deadline_elapsed" || sealed.EvaluationComplete || sealed.AdmissionAvailable || sealed.ExecutionAvailable || sealed.QualificationCredit {
		t.Fatal(sealed, e)
	}
	found := false
	for _, c := range sealed.Checks {
		if c.Name == "sample_sufficiency" && c.Status == "failed" {
			found = true
		}
	}
	if !found {
		t.Fatal("necessary sample failure erased", sealed.Checks)
	}
	// Retries may read the cycle clock, never the owner's new-issuance clock.
	onceClock := &oneEvaluationClock{at: clock.at}
	if err := service.RunPaperEvaluationCycle(ctx, backend, onceClock); err != nil || onceClock.calls != 1 {
		t.Fatal("retry sampled issuance clock", err, onceClock.calls)
	}
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			if e := service.RunPaperEvaluationCycle(ctx, backend, clock); e != nil {
				t.Error(e)
			}
		})
	}
	wg.Wait()
	var history storage.PaperEvaluationAssessmentCatalog
	if e := json.Unmarshal(request("GET", "/v1/paper-evaluation-assessments", nil, 200), &history); e != nil || len(history.Records) != 2 {
		t.Fatal("automatic cycles duplicated assessments", history, e)
	}
	var old storage.PaperEvaluationAssessment
	if e := json.Unmarshal(request("GET", "/v1/paper-evaluation-assessments/"+pending.Command.ID, nil, 200), &old); e != nil || !reflect.DeepEqual(old, pending) {
		t.Fatal("old assessment rewritten", e)
	}
	clock.panicRead = true
	supervisionOwnedInspections(t, request, base, d.Command.ReceiverID, []supervisionInspection{{"evaluation_assessment", command.ID, "/v1/paper-evaluation-assessments/" + command.ID}})
	clock.panicRead = false
	// A prior internal producer can have occupied a reserved identity. Keep that
	// pending receipt and surface the collision; never overwrite or treat it as final.
	clock.at = clock.at.Add(time.Nanosecond)
	collision := d.Command
	collision.ID = "automatic-collision-declaration"
	collision.Plan.FamilyID = "automatic-collision-family"
	collision.Plan.WindowStart = clock.at.Add(time.Hour)
	collision.Plan.WindowEnd = clock.at.Add(2 * time.Hour)
	collision.Plan.OutcomeDeadline = clock.at.Add(26 * time.Hour)
	var declared storage.PaperEvaluationDeclarationReceipt
	if e := json.Unmarshal(request("POST", "/v1/paper-evaluation-declarations", collision, 201), &declared); e != nil {
		t.Fatal(e)
	}
	// A collision in the first sorted declaration cannot starve a later owner.
	sibling := collision
	sibling.ID = "z-independent-declaration"
	sibling.Plan.FamilyID = "z-independent-family"
	clock.at = clock.at.Add(time.Nanosecond)
	var independent storage.PaperEvaluationDeclarationReceipt
	if e := json.Unmarshal(request("POST", "/v1/paper-evaluation-declarations", sibling, 201), &independent); e != nil {
		t.Fatal(e)
	}
	clock.at = collision.Plan.OutcomeDeadline.Add(time.Nanosecond)
	poisoned := &earlyEvaluationStore{PaperEvaluationWorkerStore: backend, at: collision.Plan.OutcomeDeadline.Add(-time.Nanosecond), declarationID: collision.ID}
	if e := service.RunPaperEvaluationCycle(ctx, poisoned, clock); !errors.Is(e, storage.ErrPaperEvaluationAssessmentConflict) {
		t.Fatal("early canonical receipt accepted", e)
	}
	independentPath := "/v1/paper-evaluation-assessments/" + service.AutomaticPaperEvaluationID(independent.Record)
	independentBefore := request("GET", independentPath, nil, 200)
	var independentResult storage.PaperEvaluationAssessment
	if e := json.Unmarshal(independentBefore, &independentResult); e != nil || independentResult.Status != "blocked" || !independentResult.AssessedAt.After(sibling.Plan.OutcomeDeadline) || independentResult.EvaluationComplete || independentResult.AdmissionAvailable || independentResult.ExecutionAvailable || independentResult.QualificationCredit {
		t.Fatal("collision starved independent declaration", independentResult, e)
	}
	if e := service.RunPaperEvaluationCycle(ctx, backend, clock); !errors.Is(e, storage.ErrPaperEvaluationAssessmentConflict) {
		t.Fatal("collision silently replaced", e)
	}
	if !bytes.Equal(independentBefore, request("GET", independentPath, nil, 200)) {
		t.Fatal("independent automatic retry changed receipt")
	}
	var retained storage.PaperEvaluationAssessment
	if e := json.Unmarshal(request("GET", "/v1/paper-evaluation-assessments/"+service.AutomaticPaperEvaluationID(declared.Record), nil, 200), &retained); e != nil || retained.Status != "pending" {
		t.Fatal("collision evidence overwritten", retained, e)
	}
}

type oneEvaluationClock struct {
	at    time.Time
	calls int
}

func (c *oneEvaluationClock) Now() time.Time {
	c.calls++
	if c.calls > 1 {
		panic("retry sampled owner issuance clock")
	}
	return c.at
}
func (c *oneEvaluationClock) Wait(ctx context.Context, _ time.Duration) error { return ctx.Err() }

type earlyEvaluationStore struct {
	service.PaperEvaluationWorkerStore
	at            time.Time
	declarationID string
}

func (s *earlyEvaluationStore) AssessPaperEvaluation(ctx context.Context, c storage.PaperEvaluationAssessmentCommand, now func() time.Time, de storage.PaperDecisionEvaluator, oe storage.PaperOutcomeEvaluator) (storage.PaperEvaluationAssessmentReceipt, error) {
	if c.DeclarationID == s.declarationID {
		now = func() time.Time { return s.at }
	}
	return s.PaperEvaluationWorkerStore.AssessPaperEvaluation(ctx, c, now, de, oe)
}
