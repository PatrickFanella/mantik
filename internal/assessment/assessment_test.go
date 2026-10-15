package assessment

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/experiment"
	"git.subcult.tv/subculture-collective/mantik/internal/hypothesis"
	"git.subcult.tv/subculture-collective/mantik/internal/news"
	"git.subcult.tv/subculture-collective/mantik/internal/replay"
	"git.subcult.tv/subculture-collective/mantik/internal/trial"
)

func fixture(t *testing.T, changes ...func(*hypothesis.Declaration)) trial.Evidence {
	t.Helper()
	dataset, err := replay.FixtureDataset()
	if err != nil {
		t.Fatal(err)
	}
	recipe, err := experiment.DefaultRecipe(dataset)
	if err != nil {
		t.Fatal(err)
	}
	d := hypothesis.Fixture()
	d.HorizonSeconds = int64(recipe.WindowEnd.Sub(recipe.WindowStart) / time.Second)
	d.Methods = nil
	for _, m := range recipe.Methods {
		pin, _, _ := hypothesis.PinMethod(m.ID)
		d.Methods = append(d.Methods, pin)
	}
	for _, change := range changes {
		change(&d)
	}
	sealed, err := hypothesis.Seal(d)
	if err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 10, 15, 0, 0, 0, 0, time.UTC)
	p := hypothesis.Publication{Schema: hypothesis.PublicationSchema, ID: "publication", ReceiverID: "receiver-abcdefghijklmnop", PublishedAt: at, OriginBindingVerified: true, Sealed: sealed}
	r, err := trial.Register(trial.Command{ReceiverID: p.ReceiverID, RequestID: "trial", PublicationID: p.ID, Recipe: recipe}, p, nil, at)
	if err != nil {
		t.Fatal(err)
	}
	a, err := trial.StartAttempt(trial.AttemptCommand{ReceiverID: r.ReceiverID, RequestID: "attempt", TrialID: r.ID}, r, at)
	if err != nil {
		t.Fatal(err)
	}
	input, _ := json.Marshal(dataset)
	run, err := trial.BeginRun(a, r, string(input), "invocation", at)
	if err != nil {
		t.Fatal(err)
	}
	native, err := experiment.Execute(context.Background(), run.NativeAttemptID, recipe, dataset)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(native)
	run, err = trial.FinishRun(run, r, run.InvocationID, string(raw), at.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	return trial.Evidence{Attempt: a, Registration: r, Publication: p, Run: &run, Result: string(raw)}
}
func TestActualNativeAssessment(t *testing.T) {
	e := fixture(t)
	before := trial.Hash(e)
	got, err := Build(e)
	if err != nil || got.Status != "compared" || got.QualificationCredit || len(got.Methods) != 4 {
		t.Fatal(got, err)
	}
	for i, want := range []int64{-6000, -3000, 0, -7200} {
		m := got.Methods[i]
		if m.Metrics == nil || m.Metrics.MarkedPnLCents != want {
			t.Fatal(m)
		}
		if i < 2 {
			if m.Matches == nil || m.Matches.Prediction || !m.Matches.Null || !m.Matches.Refutation {
				t.Fatal("overlapping null/refutation lost", m)
			}
		} else if m.Matches != nil || m.Role != "benchmark" {
			t.Fatal("benchmark became hypothesis evidence", m)
		}
	}
	repeated, err := Build(e)
	if err != nil || !reflect.DeepEqual(got, repeated) || trial.Hash(e) != before {
		t.Fatal("assessment changed retained records", err)
	}
	got.SHA256 = ""
	if trial.Hash(got) != repeated.SHA256 {
		t.Fatal("unstable assessment digest")
	}
}
func TestComparisonOperatorsAndMetrics(t *testing.T) {
	for _, tc := range []struct {
		op   string
		want []bool
	}{
		{"lt", []bool{true, false, false}}, {"le", []bool{true, true, false}}, {"eq", []bool{false, true, false}},
		{"ne", []bool{true, false, true}}, {"ge", []bool{false, true, true}}, {"gt", []bool{false, false, true}},
	} {
		for i, x := range []int64{-1, 0, 1} {
			if matches(hypothesis.Condition{Comparison: tc.op}, x) != tc.want[i] {
				t.Fatal(tc.op, x)
			}
		}
	}
	m := experiment.Metrics{Observations: 1, Actions: 2, RealizedPnLCents: 3, MarkedPnLCents: 4}
	for i, key := range []string{"observations", "actions", "realized_pnl_cents", "marked_pnl_cents"} {
		if metric(m, key) != int64(i+1) {
			t.Fatal(key)
		}
	}
}

func TestConditionGapsAndPredictionMatch(t *testing.T) {
	for _, tc := range []struct {
		name                         string
		prediction, null, refutation int64
		want                         Matches
	}{
		{"gap", 100, -10000, -20000, Matches{}},
		{"prediction", -7000, -8000, -9000, Matches{Prediction: true}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := fixture(t, func(d *hypothesis.Declaration) {
				d.Prediction.Threshold = tc.prediction
				d.Null.Threshold = tc.null
				d.Refutation.Threshold = tc.refutation
			})
			got, err := Build(e)
			if err != nil || got.Status != "compared" || got.Methods[0].Matches == nil || *got.Methods[0].Matches != tc.want || got.QualificationCredit {
				t.Fatal(got, err)
			}
		})
	}
}
func resealRun(r *trial.Run)     { r.SHA256 = ""; r.SHA256 = trial.Hash(*r) }
func hashText(raw string) string { return news.SHA256(raw) }
func TestNonEvaluableAttempts(t *testing.T) {
	for _, state := range []string{"pending", "cancelled", "running", "incomplete", "failed", "late"} {
		t.Run(state, func(t *testing.T) {
			e := fixture(t)
			switch state {
			case "pending":
				e.Run = nil
				e.Result = ""
			case "cancelled":
				e.Run = nil
				e.Result = ""
				e.Attempt, _ = trial.CancelAttempt(e.Attempt, e.Attempt.StartedAt)
			case "running", "incomplete":
				e.Run.Status = "running"
				e.Run.FinishedAt = time.Time{}
				e.Run.ResultSHA256 = ""
				e.Run.ResultBytes = 0
				e.Run.DeadlineMet = false
				e.Result = ""
				resealRun(e.Run)
				if state == "incomplete" {
					r, err := trial.ResolveRun(*e.Run, e.Run.Deadline)
					if err != nil {
						t.Fatal(err)
					}
					e.Run = &r
				}
			case "failed":
				// A failed run may retain partial metrics. They never acquire matches.
				var n experiment.Attempt
				_ = json.Unmarshal([]byte(e.Result), &n)
				n.Status = "failed"
				n.ErrorCode = "replay_failed"
				n.Results = n.Results[:1]
				raw, _ := json.Marshal(n)
				e.Result = string(raw)
				e.Run.Status = "failed"
				e.Run.ErrorCode = n.ErrorCode
				e.Run.ResultBytes = len(raw)
				e.Run.ResultSHA256 = trial.Hash(n)
				resealRun(e.Run)
			case "late":
				e.Run.FinishedAt = e.Run.Deadline
				e.Run.DeadlineMet = false
				resealRun(e.Run)
			}
			got, err := Build(e)
			if err != nil || got.Status != "not_evaluable" || len(got.Methods) != 4 {
				t.Fatal(got, err)
			}
			for _, m := range got.Methods {
				if m.Matches != nil {
					t.Fatal("ineligible run compared", m)
				}
			}
			if state != "failed" && state != "late" {
				for _, m := range got.Methods {
					if m.Metrics != nil {
						t.Fatal("invented zero metrics", m)
					}
				}
			}
			if state == "failed" && (got.Methods[0].Metrics == nil || got.Methods[1].Metrics != nil) {
				t.Fatal("partial result lost or fabricated")
			}
		})
	}
}
func TestBindingAndMetricCorruptionRejected(t *testing.T) {
	for _, change := range []func(*trial.Evidence){
		func(e *trial.Evidence) { e.Publication.Sealed.Declaration.Prediction.Threshold++ },
		func(e *trial.Evidence) { e.Run.IntentSHA256 = strings.Repeat("0", 64); resealRun(e.Run) },
		func(e *trial.Evidence) {
			e.Result = strings.Replace(e.Result, `"marked_pnl_cents":-6000`, `"marked_pnl_cents":null`, 1)
			e.Run.ResultSHA256 = hashText(e.Result)
			e.Run.ResultBytes = len(e.Result)
			resealRun(e.Run)
		},
		func(e *trial.Evidence) {
			e.Result = strings.Replace(e.Result, `"actions":2,`, "", 1)
			e.Run.ResultSHA256 = hashText(e.Result)
			e.Run.ResultBytes = len(e.Result)
			resealRun(e.Run)
		},
		func(e *trial.Evidence) { e.Result += " " },
		func(e *trial.Evidence) { e.Run = nil },
	} {
		e := fixture(t)
		change(&e)
		if _, err := Build(e); err == nil {
			t.Fatal("corrupt evidence accepted")
		}
	}
}
