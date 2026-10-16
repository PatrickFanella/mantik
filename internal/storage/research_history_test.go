package storage_test

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/assessment"
	"git.subcult.tv/subculture-collective/mantik/internal/researchhistory"
	"git.subcult.tv/subculture-collective/mantik/internal/storage"
	"git.subcult.tv/subculture-collective/mantik/internal/trial"
	"git.subcult.tv/subculture-collective/mantik/internal/workertrial"
)

type combinedBackend interface {
	researchhistory.Reader
	reportedBackend
	storage.ExternalHistoryStore
}
type combinedFixture struct {
	registration trial.Registration
	job          workertrial.Record
	pending      workertrial.Intent
	at           time.Time
	child        trial.Registration
	empty        trial.Registration
}

func combinedSource(t *testing.T, s combinedBackend) combinedFixture {
	t.Helper()
	ctx := context.Background()
	r, run, at := externalSource(t, s)
	now := func() time.Time { return at }
	job, err := s.DeclareExternalJob(ctx, externalCommand(r, run, "main-job"), now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.DeclareExternalJob(ctx, externalCommand(r, run, "unattempted-job"), now); err != nil {
		t.Fatal(err)
	}
	for _, status := range []string{"completed", "mismatch", "failed", "incomplete", "cancelled", "timed_out", "wait_interrupted", "pending"} {
		i, err := s.StartExternalAttempt(ctx, workertrial.AttemptCommand{ReceiverID: r.ReceiverID, RequestID: "external-" + status, JobID: job.Command.RequestID, DeclarationSHA256: job.SHA256}, now)
		if err != nil {
			t.Fatal(err)
		}
		if status == "pending" {
			continue
		}
		body := "{partial output\n"
		v := reportedFixture(t, job.Job)
		if status == "mismatch" {
			item := v["results"].([]any)[0].(map[string]any)
			item["metrics"].(map[string]int64)["marked_pnl_cents"]++
			c := item["comparisons"].(map[string]any)["marked_pnl_cents"].(map[string]any)
			c["external"] = item["metrics"].(map[string]int64)["marked_pnl_cents"]
			c["delta"] = 1
			c["matches"] = false
			v["all_comparable_metrics_match"] = false
		}
		if status == "failed" {
			v["status"] = "failed"
			v["error_code"] = "library_execution_failed"
			v["results"] = v["results"].([]any)[:1]
			v["all_comparable_metrics_match"] = false
		}
		if status == "completed" || status == "mismatch" || status == "failed" {
			body = string(reportedBytes(v))
		}
		if _, err = s.ReportExternalAttempt(ctx, workertrial.ReportCommand{ReceiverID: r.ReceiverID, AttemptID: i.Command.RequestID, IntentSHA256: i.SHA256, ReportedStatus: status, Stdout: body, Stderr: "capture\n"}, now); err != nil {
			t.Fatal(err)
		}
	}
	for _, id := range []string{"native-pending", "native-cancelled", "native-failed"} {
		if _, err = s.StartTrialAttempt(ctx, trial.AttemptCommand{ReceiverID: r.ReceiverID, RequestID: id, TrialID: r.ID}, now); err != nil {
			t.Fatal(err)
		}
	}
	if _, err = s.CancelTrialAttempt(ctx, r.ReceiverID, "native-cancelled", now); err != nil {
		t.Fatal(err)
	}
	input, err := s.TrialRunArtifact(ctx, run.AttemptID, "input")
	if err != nil {
		t.Fatal(err)
	}
	work, err := s.BeginTrialRun(ctx, r.ReceiverID, "native-failed", input.Content, "failure-owner", now)
	if err != nil {
		t.Fatal(err)
	}
	failed := map[string]any{"id": work.Run.NativeAttemptID, "recipe_sha256": work.Run.RecipeSHA256, "input_sha256": work.Run.InputSHA256, "status": "failed", "error_code": "dataset_pin_mismatch", "evidence_class": "synthetic_unqualified", "qualified": false, "results": []any{}}
	if _, err = s.FinishTrialRun(ctx, r.ReceiverID, "native-failed", "failure-owner", string(jsonBytes(failed)), now); err != nil {
		t.Fatal(err)
	}
	child, err := s.RegisterTrial(ctx, trial.Command{ReceiverID: r.ReceiverID, RequestID: "child", PublicationID: r.PublicationID, ParentID: r.ID, Recipe: r.Recipe}, now)
	if err != nil {
		t.Fatal(err)
	}
	empty, err := s.RegisterTrial(ctx, trial.Command{ReceiverID: r.ReceiverID, RequestID: "empty-grandchild", PublicationID: r.PublicationID, ParentID: child.ID, Recipe: r.Recipe}, now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.StartTrialAttempt(ctx, trial.AttemptCommand{ReceiverID: r.ReceiverID, RequestID: "native-late", TrialID: child.ID}, now); err != nil {
		t.Fatal(err)
	}
	lateWork, err := s.BeginTrialRun(ctx, r.ReceiverID, "native-late", input.Content, "late-owner", now)
	if err != nil {
		t.Fatal(err)
	}
	original, err := s.TrialRunArtifact(ctx, run.AttemptID, "result")
	if err != nil {
		t.Fatal(err)
	}
	var value map[string]any
	if err = json.Unmarshal([]byte(original.Content), &value); err != nil {
		t.Fatal(err)
	}
	value["id"] = lateWork.Run.NativeAttemptID
	at = at.Add(31 * time.Second)
	late, err := s.FinishTrialRun(ctx, r.ReceiverID, "native-late", "late-owner", string(jsonBytes(value)), now)
	if err != nil || late.DeadlineMet {
		t.Fatal(err)
	}
	if _, err = s.DeclareExternalJob(ctx, externalCommand(child, late, "late-source-job"), now); err != nil {
		t.Fatal(err)
	}
	pending, err := s.StartExternalAttempt(ctx, workertrial.AttemptCommand{ReceiverID: r.ReceiverID, RequestID: "external-pending", JobID: job.Command.RequestID, DeclarationSHA256: job.SHA256}, func() time.Time { panic("fixture retry clock") })
	if err != nil {
		t.Fatal(err)
	}
	return combinedFixture{r, job, pending, at, child, empty}
}
func TestMemoryCombinedResearchHistory(t *testing.T) { combinedContracts(t, storage.NewMemory(), "") }
func TestPostgresContractsCombinedResearchHistory(t *testing.T) {
	s, config := externalPostgres(t, "combinedhistory")
	combinedContracts(t, s, config)
}
func combinedContracts(t *testing.T, s combinedBackend, config string) {
	ctx := context.Background()
	f := combinedSource(t, s)
	before, _ := s.Snapshot(ctx)
	native, err := assessment.ReadHistory(ctx, s, f.registration.ID)
	if err != nil {
		t.Fatal(err)
	}
	h, err := researchhistory.ReadTrial(ctx, s, f.registration.ID)
	if err != nil || h.Validate() != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(native, h.Native) || h.Native.AttemptCount != 4 || h.Native.Compared != 1 || h.External.JobCount != 2 || h.External.UnattemptedJobs != 1 || h.External.AttemptCount != 8 || h.External.Pending != 1 || h.External.Reported != 7 {
		t.Fatal(h)
	}
	for _, status := range []string{"completed", "mismatch", "failed", "incomplete", "cancelled", "timed_out", "wait_interrupted"} {
		if h.External.ReportedStatuses[status] != 1 {
			t.Fatal(status)
		}
	}
	// Matching external/native metrics include a refuted synthetic hypothesis.
	for _, a := range h.Native.Assessments {
		if a.Status == "compared" && (!a.Methods[0].Matches.Null || !a.Methods[0].Matches.Refutation || a.Methods[0].Matches.Prediction) {
			t.Fatal("native condition evidence changed")
		}
	}
	q, err := researchhistory.ReadQuestion(ctx, s, f.registration.HypothesisID)
	if err != nil || q.Validate() != nil || q.Native.PlanCount != 3 || q.Native.NeverAttemptedPlans != 1 || q.Native.AttemptCount != 5 || q.Native.Compared != 1 || q.ExternalJobCount != 3 || q.UnattemptedExternalJobs != 2 || q.ExternalAttemptCount != 8 || q.PendingExternal != 1 || q.ReportedExternal != 7 || q.QualificationCredit {
		t.Fatal(q, err)
	}
	repeated, err := researchhistory.ReadQuestion(ctx, s, f.registration.HypothesisID)
	if err != nil || !reflect.DeepEqual(repeated, q) {
		t.Fatal("unchanged history unstable", err)
	}
	late, err := researchhistory.ReadTrial(ctx, s, f.child.ID)
	if err != nil || late.Native.Assessments[0].ReasonCode != "run_completed_after_deadline" || late.External.Jobs[0].Declaration.NativeDeadlineMet {
		t.Fatal(late, err)
	}
	empty, err := researchhistory.ReadTrial(ctx, s, f.empty.ID)
	if err != nil || empty.Native.AttemptCount != 0 || empty.External.JobCount != 0 || empty.Validate() != nil {
		t.Fatal(empty, err)
	}
	if pg, ok := s.(*storage.Postgres); ok {
		pg.Close()
		reopened, err := storage.OpenPostgres(ctx, config)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(reopened.Close)
		s = reopened
		after, err := researchhistory.ReadQuestion(ctx, s, f.registration.HypothesisID)
		if err != nil || !reflect.DeepEqual(q, after) {
			t.Fatal("restart changed combined history", err)
		}
	}
	for _, change := range []func(*researchhistory.QuestionHistory){func(q *researchhistory.QuestionHistory) { q.ExternalAttemptCount-- }, func(q *researchhistory.QuestionHistory) { q.External = q.External[:2] }, func(q *researchhistory.QuestionHistory) { q.External[0], q.External[1] = q.External[1], q.External[0] }, func(q *researchhistory.QuestionHistory) { q.ReportedStatuses["mismatch"] = 0 }, func(q *researchhistory.QuestionHistory) { q.QualificationCredit = true }} {
		var bad researchhistory.QuestionHistory
		_ = json.Unmarshal(jsonBytes(q), &bad)
		change(&bad)
		bad.SHA256 = ""
		bad.SHA256 = trial.Hash(bad)
		if bad.Validate() == nil {
			t.Fatal("resealed corrupt history accepted")
		}
	}
	// A self-consistent but changed native source receipt cannot be joined to
	// the still-valid external catalogue for the original source.
	var bad researchhistory.TrialHistory
	_ = json.Unmarshal(jsonBytes(h), &bad)
	sourceID := bad.External.Jobs[0].NativeRun.AttemptID
	for i := range bad.Native.Index.Attempts {
		if bad.Native.Index.Attempts[i].AttemptID == sourceID {
			bad.Native.Index.Attempts[i].RunSHA256 = strings.Repeat("d", 64)
		}
	}
	bad.Native.Index.SHA256 = ""
	bad.Native.Index.SHA256 = trial.Hash(bad.Native.Index)
	for i := range bad.Native.Assessments {
		a := &bad.Native.Assessments[i]
		if a.AttemptID == sourceID {
			a.RunSHA256 = strings.Repeat("d", 64)
			a.SHA256 = ""
			a.SHA256 = trial.Hash(*a)
		}
	}
	bad.Native.SHA256 = ""
	bad.Native.SHA256 = trial.Hash(bad.Native)
	bad.SHA256 = ""
	bad.SHA256 = trial.Hash(bad)
	if bad.Native.Validate() != nil || bad.External.Validate() != nil || bad.Validate() == nil {
		t.Fatal("changed native source accepted by join")
	}
	after, _ := s.Snapshot(ctx)
	if !reflect.DeepEqual(before, after) {
		t.Fatal("history read mutated operations")
	}
	if _, err = researchhistory.ReadTrial(ctx, s, "missing"); !errors.Is(err, trial.ErrNotFound) {
		t.Fatal(err)
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err = researchhistory.ReadQuestion(cancelled, s, f.registration.HypothesisID); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}

type changingResearch struct {
	combinedBackend
	externalCalls    int
	after            func(string, int)
	evidenceMutation func()
}

func (s *changingResearch) ExternalTrialHistory(ctx context.Context, id string) (workertrial.History, error) {
	h, err := s.combinedBackend.ExternalTrialHistory(ctx, id)
	s.externalCalls++
	if s.after != nil {
		s.after(id, s.externalCalls)
	}
	return h, err
}
func (s *changingResearch) TrialEvidence(ctx context.Context, id string) (trial.Evidence, error) {
	e, err := s.combinedBackend.TrialEvidence(ctx, id)
	if s.evidenceMutation != nil {
		f := s.evidenceMutation
		s.evidenceMutation = nil
		f()
	}
	return e, err
}
func TestCombinedHistoryRejectsConcurrentChanges(t *testing.T) {
	for _, scope := range []string{"trial", "question"} {
		for _, mutation := range []string{"report", "job", "native", "plan"} {
			if scope == "trial" && mutation == "plan" {
				continue
			}
			t.Run(scope+"-"+mutation, func(t *testing.T) {
				s := storage.NewMemory()
				f := combinedSource(t, s)
				ctx := context.Background()
				now := func() time.Time { return f.at }
				wrapper := &changingResearch{combinedBackend: s}
				done := false
				wrapper.after = func(id string, n int) {
					if done || id != f.registration.ID || scope == "trial" && mutation == "native" {
						return
					}
					done = true
					var err error
					switch mutation {
					case "report":
						_, err = s.ReportExternalAttempt(ctx, workertrial.ReportCommand{ReceiverID: f.registration.ReceiverID, AttemptID: f.pending.Command.RequestID, IntentSHA256: f.pending.SHA256, ReportedStatus: "incomplete", Stdout: "{late capture"}, now)
					case "job":
						_, err = s.DeclareExternalJob(ctx, externalCommand(f.registration, firstNativeRun(t, s), "added-job"), now)
					case "native":
						_, err = s.CancelTrialAttempt(ctx, f.registration.ReceiverID, "native-pending", now)
					case "plan":
						_, err = s.RegisterTrial(ctx, trial.Command{ReceiverID: f.registration.ReceiverID, RequestID: "added-plan", PublicationID: f.registration.PublicationID, Recipe: f.registration.Recipe}, now)
					}
					if err != nil {
						t.Fatal(err)
					}
				}
				if scope == "trial" && mutation == "native" {
					wrapper.evidenceMutation = func() {
						if _, err := s.CancelTrialAttempt(ctx, f.registration.ReceiverID, "native-pending", now); err != nil {
							t.Fatal(err)
						}
					}
				}

				if scope == "trial" {
					h, err := researchhistory.ReadTrial(ctx, wrapper, f.registration.ID)
					if !errors.Is(err, assessment.ErrHistoryChanged) || !reflect.DeepEqual(h, researchhistory.TrialHistory{}) {
						t.Fatal("mixed trial history returned", err)
					}
				} else {
					q, err := researchhistory.ReadQuestion(ctx, wrapper, f.registration.HypothesisID)
					if !errors.Is(err, assessment.ErrHistoryChanged) || !reflect.DeepEqual(q, researchhistory.QuestionHistory{}) {
						t.Fatal("mixed hypothesis history returned", err)
					}
				}
			})
		}
	}
}
func firstNativeRun(t *testing.T, s storage.TrialRunStore) trial.Run {
	t.Helper()
	r, err := s.TrialRun(context.Background(), "native-source")
	if err != nil {
		t.Fatal(err)
	}
	return r
}
