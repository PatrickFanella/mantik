package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/assessment"
	"git.subcult.tv/subculture-collective/mantik/internal/experiment"
	"git.subcult.tv/subculture-collective/mantik/internal/hypothesis"
	"git.subcult.tv/subculture-collective/mantik/internal/investigation"
	"git.subcult.tv/subculture-collective/mantik/internal/news"
	"git.subcult.tv/subculture-collective/mantik/internal/replay"
	"git.subcult.tv/subculture-collective/mantik/internal/service"
	"git.subcult.tv/subculture-collective/mantik/internal/storage"
	"git.subcult.tv/subculture-collective/mantik/internal/trial"
	"git.subcult.tv/subculture-collective/mantik/internal/workertrial"
)

func TestTrialRegistrationCLIAndAPI(t *testing.T) {
	ctx := context.Background()
	s := storage.NewMemory()
	catalog, _ := s.NewsCases(ctx)
	source := news.Fixture()
	admitted, err := s.AdmitNews(ctx, catalog.ReceiverID, source, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	started, err := s.StartReview(ctx, catalog.ReceiverID, "review", admitted.Matches[0].CaseID, 1, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	data, err := replay.FixtureDataset()
	if err != nil {
		t.Fatal(err)
	}
	recipe, err := experiment.DefaultRecipe(data)
	if err != nil {
		t.Fatal(err)
	}
	d := hypothesis.Fixture()
	d.HorizonSeconds = int64(recipe.WindowEnd.Sub(recipe.WindowStart) / time.Second)
	d.Methods = nil
	for _, m := range recipe.Methods {
		pin, _, err := hypothesis.PinMethod(m.ID)
		if err != nil {
			t.Fatal(err)
		}
		d.Methods = append(d.Methods, pin)
	}
	claim := started.Attempt.Pin.Group.Claims[0]
	d.Origins = []hypothesis.Origin{{Reference: claim.Reference, ClaimKey: claim.Claim.Key, RecordingSHA256: claim.Claim.RecordingSHA256}}
	tool, _ := json.Marshal(investigation.Step{Kind: "tool", Tool: &investigation.ToolCall{Name: "inspect_news_claim", ClaimKey: claim.Claim.Key}})
	proposal, _ := json.Marshal(investigation.Step{Kind: "proposal", Proposal: &investigation.Proposal{Outcome: "propose_hypothesis", Uncertainty: "unverified", ReasonCodes: []string{"synthetic_question"}, ClaimKeys: []string{claim.Claim.Key}, Candidate: &investigation.Candidate{Kind: "research_hypothesis", ClaimCode: d.Mechanism.Code, RefutationCode: d.Refutation.Code, EvidenceKeys: []string{claim.Claim.Key}}}})
	if _, err = s.CompleteReview(ctx, catalog.ReceiverID, "review", []json.RawMessage{tool, proposal}, time.Now); err != nil {
		t.Fatal(err)
	}
	publication, err := s.PublishHypothesis(ctx, catalog.ReceiverID, "publication", "review", d, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(service.Handler(s, "trial-cli-token"))
	defer server.Close()
	t.Setenv("MANTIK_API_TOKEN", "trial-cli-token")
	file := filepath.Join(t.TempDir(), "recipe.json")
	raw, _ := json.Marshal(recipe)
	if err = os.WriteFile(file, raw, 0600); err != nil {
		t.Fatal(err)
	}
	run := func(args ...string) []byte {
		t.Helper()
		var out bytes.Buffer
		args = append(args, "--api-url", server.URL)
		if err := Run(ctx, args, &out, &out); err != nil {
			t.Fatal(args, err)
		}
		return out.Bytes()
	}
	args := []string{"hypothesis-trial-register", "--id", "first", "--receiver-id", catalog.ReceiverID, "--publication", publication.ID, "--recipe", file, "--recipe-sha256", recipe.SHA256}
	var questionHistory assessment.QuestionHistory
	if err = json.Unmarshal(run("hypothesis-assessments", "--id", publication.Sealed.ID), &questionHistory); err != nil || questionHistory.Validate() != nil || questionHistory.PlanCount != 0 {
		t.Fatal(questionHistory, err)
	}
	var first, repeated, actual trial.Registration
	if err = json.Unmarshal(run(args...), &first); err != nil || first.Validate() != nil {
		t.Fatal(err)
	}
	if err = json.Unmarshal(run(args...), &repeated); err != nil || !reflect.DeepEqual(first, repeated) {
		t.Fatal("retry changed receipt", err)
	}
	if err = json.Unmarshal(run("hypothesis-trial", "--id", "first"), &actual); err != nil || !reflect.DeepEqual(first, actual) {
		t.Fatal("inspection changed receipt", err)
	}
	var history trial.History
	if err = json.Unmarshal(run("hypothesis-trials", "--id", publication.Sealed.ID), &history); err != nil || len(history.Registrations) != 1 || history.Registrations[0].RecipeSHA256 != recipe.SHA256 {
		t.Fatal(history, err)
	}
	if b, err := os.ReadFile(file); err != nil || !bytes.Equal(b, raw) {
		t.Fatal("CLI modified recipe", err)
	}
	intentArgs := []string{"trial-attempt-start", "--id", "intent", "--trial", first.ID, "--receiver-id", catalog.ReceiverID}
	var intent, cancelledIntent, retriedIntent trial.Attempt
	if err = json.Unmarshal(run(intentArgs...), &intent); err != nil || intent.Validate() != nil || intent.ExecutionStarted {
		t.Fatal(intent, err)
	}
	if err = json.Unmarshal(run("trial-attempt-cancel", "--id", "intent", "--receiver-id", catalog.ReceiverID), &cancelledIntent); err != nil || cancelledIntent.Status != "cancelled" || cancelledIntent.IntentSHA256 != intent.IntentSHA256 {
		t.Fatal(cancelledIntent, err)
	}
	if err = json.Unmarshal(run(intentArgs...), &retriedIntent); err != nil || !reflect.DeepEqual(cancelledIntent, retriedIntent) {
		t.Fatal("start retry resurrected cancelled intent", err)
	}
	var intentHistory trial.AttemptHistory
	if err = json.Unmarshal(run("trial-attempts", "--id", first.ID), &intentHistory); err != nil || len(intentHistory.Attempts) != 1 || !reflect.DeepEqual(intentHistory.Attempts[0], cancelledIntent) {
		t.Fatal(intentHistory, err)
	}
	if err = json.Unmarshal(run("trial-attempt", "--id", "intent"), &retriedIntent); err != nil || !reflect.DeepEqual(cancelledIntent, retriedIntent) {
		t.Fatal(err)
	}
	run("trial-attempt-start", "--id", "native", "--trial", first.ID, "--receiver-id", catalog.ReceiverID)
	var native trial.Run
	executeArgs := []string{"trial-run-execute", "--id", "native", "--receiver-id", catalog.ReceiverID}
	if err = json.Unmarshal(run(executeArgs...), &native); err != nil || native.Status != "completed" || native.Validate() != nil || native.QualificationCredit {
		t.Fatal(native, err)
	}
	var nativeRetry trial.Run
	t.Run("external-worker-handoff", func(t *testing.T) { testTrialWorkerHandoff(t, ctx, server.URL, catalog.ReceiverID, native) })
	if err = json.Unmarshal(run(executeArgs...), &nativeRetry); err != nil || !reflect.DeepEqual(native, nativeRetry) {
		t.Fatal("native retry reran execution", err)
	}
	var artifact trial.RunArtifact
	if err = json.Unmarshal(run("trial-run", "--id", "native", "--artifact", "result"), &artifact); err != nil || artifact.SHA256 != native.ResultSHA256 {
		t.Fatal(err)
	}
	var executed experiment.Attempt
	if err = json.Unmarshal([]byte(artifact.Content), &executed); err != nil || executed.Qualified || len(executed.Results) != 4 {
		t.Fatal(executed, err)
	}
	for i, want := range []int64{-6000, -3000, 0, -7200} {
		if executed.Results[i].Metrics.MarkedPnLCents != want {
			t.Fatal("native metrics changed")
		}
	}
	var compared assessment.Result
	if err = json.Unmarshal(run("trial-assessment", "--id", "native"), &compared); err != nil || compared.Status != "compared" || len(compared.Methods) != 4 || !compared.Methods[0].Matches.Null || !compared.Methods[0].Matches.Refutation || compared.Methods[2].Matches != nil || compared.NativeResultSHA256 != native.ResultSHA256 {
		t.Fatal(compared, err)
	}
	compared = assessment.Result{}
	if err = json.Unmarshal(run("trial-assessment", "--id", "intent"), &compared); err != nil || compared.ReasonCode != "cancelled_before_execution" || compared.Methods[0].Metrics != nil {
		t.Fatal(compared, err)
	}
	var assessmentHistory assessment.History
	if err = json.Unmarshal(run("trial-assessments", "--id", first.ID), &assessmentHistory); err != nil || assessmentHistory.Validate() != nil || assessmentHistory.AttemptCount != 2 || assessmentHistory.Compared != 1 || assessmentHistory.NotEvaluable != 1 {
		t.Fatal(assessmentHistory, err)
	}
	for _, p := range []struct{ id, parent string }{{"child", "first"}, {"sibling", "first"}, {"never-attempted", "child"}} {
		run("hypothesis-trial-register", "--id", p.id, "--receiver-id", catalog.ReceiverID, "--publication", publication.ID, "--recipe", file, "--recipe-sha256", recipe.SHA256, "--parent", p.parent)
	}
	questionHistory = assessment.QuestionHistory{}
	if err = json.Unmarshal(run("hypothesis-assessments", "--id", publication.Sealed.ID), &questionHistory); err != nil || questionHistory.Validate() != nil || questionHistory.PlanCount != 4 || questionHistory.NeverAttemptedPlans != 3 || questionHistory.AttemptCount != 2 || questionHistory.Compared != 1 {
		t.Fatal(questionHistory, err)
	}
	run("trial-attempt-start", "--id", "pending-for-history", "--trial", first.ID, "--receiver-id", catalog.ReceiverID)
	changing := &changingHistoryStore{Memory: s, before: func() {
		if _, err := s.CancelTrialAttempt(ctx, catalog.ReceiverID, "pending-for-history", time.Now); err != nil {
			t.Error(err)
		}
	}}
	changedServer := httptest.NewServer(service.Handler(changing, "trial-cli-token"))
	defer changedServer.Close()
	var changedOut bytes.Buffer
	if err = Run(ctx, []string{"trial-assessments", "--api-url", changedServer.URL, "--id", first.ID}, &changedOut, &changedOut); err == nil || !strings.Contains(err.Error(), "HTTP 409") || changedOut.Len() != 0 {
		t.Fatal("mixed history was accepted or printed", err, changedOut.String())
	}
	run("trial-attempt-start", "--id", "question-pending", "--trial", first.ID, "--receiver-id", catalog.ReceiverID)
	changingQuestion := &changingHistoryStore{Memory: s, before: func() {
		if _, err := s.CancelTrialAttempt(ctx, catalog.ReceiverID, "question-pending", time.Now); err != nil {
			t.Error(err)
		}
	}}
	changedQuestionServer := httptest.NewServer(service.Handler(changingQuestion, "trial-cli-token"))
	defer changedQuestionServer.Close()
	changedOut.Reset()
	if err = Run(ctx, []string{"hypothesis-assessments", "--api-url", changedQuestionServer.URL, "--id", publication.Sealed.ID}, &changedOut, &changedOut); err == nil || !strings.Contains(err.Error(), "HTTP 409") || changedOut.Len() != 0 {
		t.Fatal("partial question report accepted or printed", err, changedOut.String())
	}
	command := trial.Command{ReceiverID: catalog.ReceiverID, RequestID: "second", PublicationID: publication.ID, Recipe: recipe}
	body, _ := json.Marshal(command)
	call := func(method, path, body string, auth bool, want int) {
		t.Helper()
		req, _ := http.NewRequestWithContext(ctx, method, server.URL+path, strings.NewReader(body))
		if auth {
			req.Header.Set("Authorization", "Bearer trial-cli-token")
		}
		res, err := server.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		if res.StatusCode != want {
			t.Fatal(path, res.StatusCode, want)
		}
	}
	intentBody, _ := json.Marshal(trial.AttemptCommand{ReceiverID: catalog.ReceiverID, RequestID: "intent-two", TrialID: first.ID})
	workerBody, _ := json.Marshal(workertrial.Command{ReceiverID: catalog.ReceiverID, RequestID: "external-next", AttemptID: native.AttemptID, NativeRunSHA256: native.SHA256, ImageSHA256: "sha256:" + strings.Repeat("a", 64), AdapterSHA256: strings.Repeat("b", 64), RequirementsSHA256: strings.Repeat("c", 64)})
	call("POST", "/v1/external-trial-jobs", string(workerBody), false, 401)
	call("GET", "/v1/external-trial-jobs/external", "", false, 401)
	call("GET", "/v1/external-trial-jobs/external/inputs/native.json", "", false, 401)
	call("POST", "/v1/external-trial-jobs", strings.Replace(string(workerBody), `"request_id":`, `"qualification_credit":true,"request_id":`, 1), true, 422)
	call("POST", "/v1/external-trial-jobs", strings.Replace(string(workerBody), `"request_id":`, `"request_id":"duplicate","request_id":`, 1), true, 422)
	call("GET", "/v1/external-trial-jobs/external?launch=true", "", true, 422)
	call("GET", "/v1/external-trial-jobs/missing", "", true, 404)
	call("GET", "/v1/external-trial-jobs/external/inputs/other.json", "", true, 422)
	call("POST", "/v1/external-trial-jobs/external/launch", `{}`, true, 404)
	call("POST", "/v1/trial-attempts", string(intentBody), false, 401)
	call("GET", "/v1/trial-attempts/intent", "", false, 401)
	call("POST", "/v1/trial-attempts", strings.Replace(string(intentBody), `"trial_id":`, `"result":{},"trial_id":`, 1), true, 422)
	call("POST", "/v1/trial-attempts", strings.Replace(string(intentBody), `"request_id":`, `"request_id":"changed","request_id":`, 1), true, 422)
	call("POST", "/v1/trial-attempts/intent/cancel", `{"receiver_id":"`+catalog.ReceiverID+`","execution_started":true}`, true, 422)
	call("GET", "/v1/trial-attempts/intent?result=true", "", true, 422)
	call("GET", "/v1/hypothesis-trials/missing/attempts", "", true, 404)
	call("POST", "/v1/trial-attempts/native/run", `{"receiver_id":"`+catalog.ReceiverID+`"}`, false, 401)
	call("POST", "/v1/trial-attempts/native/run", `{"receiver_id":"`+catalog.ReceiverID+`","result":{}}`, true, 422)
	call("POST", "/v1/trial-attempts/intent/run", `{"receiver_id":"`+catalog.ReceiverID+`"}`, true, 409)
	call("POST", "/v1/trial-attempts/native/cancel", `{"receiver_id":"`+catalog.ReceiverID+`"}`, true, 409)
	call("GET", "/v1/trial-attempts/native/run?result=true", "", true, 422)
	call("GET", "/v1/trial-attempts/native/assessment", "", false, 401)
	call("GET", "/v1/trial-attempts/native/assessment?best=true", "", true, 422)
	call("GET", "/v1/trial-attempts/missing/assessment", "", true, 404)
	call("POST", "/v1/trial-attempts/native/assessment", `{}`, true, 405)
	call("GET", "/v1/hypothesis-trials/first/assessments", "", false, 401)
	call("GET", "/v1/hypothesis-trials/first/assessments?best=true", "", true, 422)
	call("GET", "/v1/hypothesis-trials/missing/assessments", "", true, 404)
	call("POST", "/v1/hypothesis-trials/first/assessments", `{}`, true, 405)
	call("GET", "/v1/hypotheses/"+publication.Sealed.ID+"/assessments", "", false, 401)
	call("GET", "/v1/hypotheses/"+publication.Sealed.ID+"/assessments?best=true", "", true, 422)
	call("GET", "/v1/hypotheses/missing/assessments", "", true, 422)
	call("GET", "/v1/hypotheses/hypothesis-"+strings.Repeat("0", 64)+"/assessments", "", true, 404)
	call("POST", "/v1/hypotheses/"+publication.Sealed.ID+"/assessments", `{}`, true, 405)
	call("POST", "/v1/hypothesis-trials", string(body), false, 401)
	call("GET", "/v1/hypothesis-trials/first", "", false, 401)
	call("POST", "/v1/hypothesis-trials", strings.Replace(string(body), `"request_id":`, `"request_id":"changed","request_id":`, 1), true, 422)
	call("POST", "/v1/hypothesis-trials", strings.Replace(string(body), `"recipe":`, `"qualification_credit":true,"recipe":`, 1), true, 422)
	call("POST", "/v1/hypothesis-trials", strings.Replace(string(body), `"ID":`, `"id":`, 1), true, 422)
	call("POST", "/v1/hypothesis-trials", strings.Replace(string(body), `"publication_id":"publication"`, `"publication_id":"missing"`, 1), true, 404)
	call("GET", "/v1/hypothesis-trials/first?original=true", "", true, 422)
	call("GET", "/v1/hypothesis-trials/missing", "", true, 404)
	call("GET", "/v1/hypotheses/"+publication.Sealed.ID+"/trials?recipe=true", "", true, 422)
	for _, bad := range [][]string{{"hypothesis-trial"}, {"hypothesis-trials", "--id", "first"}, {"hypothesis-trial-register", "--id", "first"}} {
		var out bytes.Buffer
		if err = Run(ctx, bad, &out, &out); err == nil {
			t.Fatal("incomplete command accepted")
		}
	}
}

type changingHistoryStore struct {
	*storage.Memory
	once   sync.Once
	before func()
}

func (s *changingHistoryStore) TrialEvidence(ctx context.Context, id string) (trial.Evidence, error) {
	s.once.Do(s.before)
	return s.Memory.TrialEvidence(ctx, id)
}
