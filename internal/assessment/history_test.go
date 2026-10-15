package assessment

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"testing"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/experiment"
	"git.subcult.tv/subculture-collective/mantik/internal/replay"
	"git.subcult.tv/subculture-collective/mantik/internal/trial"
)

type historyReader struct {
	registration trial.Registration
	evidence     map[string]trial.Evidence
	indexReads   int
	onIndex      func(int)
	onEvidence   func(string)
}

func (s *historyReader) TrialEvidenceIndex(ctx context.Context, id string) (trial.EvidenceIndex, error) {
	if err := ctx.Err(); err != nil {
		return trial.EvidenceIndex{}, err
	}
	s.indexReads++
	if s.onIndex != nil {
		s.onIndex(s.indexReads)
	}
	if id != s.registration.ID {
		return trial.EvidenceIndex{}, trial.ErrNotFound
	}
	a := []trial.Attempt{}
	runs := []trial.Run{}
	for _, e := range s.evidence {
		a = append(a, e.Attempt)
		if e.Run != nil {
			runs = append(runs, *e.Run)
		}
	}
	return trial.MakeEvidenceIndex(s.registration, a, runs)
}
func (s *historyReader) TrialEvidence(ctx context.Context, id string) (trial.Evidence, error) {
	if err := ctx.Err(); err != nil {
		return trial.Evidence{}, err
	}
	if s.onEvidence != nil {
		s.onEvidence(id)
	}
	e, ok := s.evidence[id]
	if !ok {
		return e, trial.ErrNotFound
	}
	return e, nil
}
func historyEvidence(t *testing.T, base trial.Evidence, id, status string) trial.Evidence {
	t.Helper()
	e := base
	a, err := trial.StartAttempt(trial.AttemptCommand{ReceiverID: e.Registration.ReceiverID, RequestID: id, TrialID: e.Registration.ID}, e.Registration, e.Attempt.StartedAt)
	if err != nil {
		t.Fatal(err)
	}
	e.Attempt = a
	e.Run = nil
	e.Result = ""
	if status == "pending" {
		return e
	}
	if status == "cancelled" {
		e.Attempt, err = trial.CancelAttempt(a, a.StartedAt)
		if err != nil {
			t.Fatal(err)
		}
		return e
	}
	dataset, _ := replay.FixtureDataset()
	input, _ := json.Marshal(dataset)
	run, err := trial.BeginRun(a, e.Registration, string(input), "owner", a.StartedAt)
	if err != nil {
		t.Fatal(err)
	}
	if status == "running" {
		e.Run = &run
		return e
	}
	if status == "incomplete" {
		run, err = trial.ResolveRun(run, run.Deadline)
		if err != nil {
			t.Fatal(err)
		}
		e.Run = &run
		return e
	}
	// Rebind the real engine fixture to distinct retained attempt identities.
	var native experiment.Attempt
	_ = json.Unmarshal([]byte(base.Result), &native)
	native.ID = run.NativeAttemptID
	if status == "failed" {
		native.Status = "failed"
		native.ErrorCode = "replay_failed"
		native.Results = native.Results[:1]
	}
	raw, _ := json.Marshal(native)
	at := run.StartedAt.Add(time.Second)
	if status == "late" {
		at = run.Deadline
	}
	run, err = trial.FinishRun(run, e.Registration, run.InvocationID, string(raw), at)
	if err != nil {
		t.Fatal(err)
	}
	e.Run = &run
	e.Result = string(raw)
	return e
}
func TestHistoryRetainsEveryAttemptAndNoWinner(t *testing.T) {
	base := fixture(t)
	s := &historyReader{registration: base.Registration, evidence: map[string]trial.Evidence{}}
	for _, status := range []string{"completed", "failed", "running", "incomplete", "pending", "cancelled", "late"} {
		s.evidence[status] = historyEvidence(t, base, status, status)
	}
	h, err := ReadHistory(context.Background(), s, base.Registration.ID)
	if err != nil || h.Validate() != nil || h.AttemptCount != 7 || h.Compared != 1 || h.NotEvaluable != 6 || len(h.Reasons) != 7 || h.QualificationCredit {
		t.Fatal(h, err)
	}
	for _, r := range h.Assessments {
		want, err := Build(s.evidence[r.AttemptID])
		if err != nil || !reflect.DeepEqual(r, want) {
			t.Fatal("history omitted or changed targeted assessment", err)
		}
	}
	repeated, err := ReadHistory(context.Background(), s, base.Registration.ID)
	if err != nil || !reflect.DeepEqual(h, repeated) {
		t.Fatal("unstable history", err)
	}
	h.Compared++
	h.SHA256 = ""
	h.SHA256 = trial.Hash(h)
	if h.Validate() == nil {
		t.Fatal("inconsistent counters accepted")
	}
}
func TestHistoryEmptyAndCapacity(t *testing.T) {
	base := fixture(t)
	s := &historyReader{registration: base.Registration, evidence: map[string]trial.Evidence{}}
	h, err := ReadHistory(context.Background(), s, base.Registration.ID)
	if err != nil || h.Validate() != nil || h.AttemptCount != 0 || h.Assessments == nil || h.Reasons == nil {
		t.Fatal(h, err)
	}
	for i := range trial.MaxAttempts {
		id := fmt.Sprintf("intent-%02d", i)
		s.evidence[id] = historyEvidence(t, base, id, "pending")
	}
	h, err = ReadHistory(context.Background(), s, base.Registration.ID)
	if err != nil || h.AttemptCount != 32 || h.NotEvaluable != 32 || h.Reasons["not_executed"] != 32 {
		t.Fatal(h, err)
	}
	if _, err = ReadHistory(context.Background(), s, "missing"); !errors.Is(err, trial.ErrNotFound) {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err = ReadHistory(ctx, s, base.Registration.ID); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}
func TestHistoryRejectsChangesWithoutPartialOutput(t *testing.T) {
	for _, mutation := range []string{"start_before_read", "cancel_before_read", "finish_after_read", "new_attempt_after_read", "missing_attempt"} {
		t.Run(mutation, func(t *testing.T) {
			base := fixture(t)
			s := &historyReader{registration: base.Registration, evidence: map[string]trial.Evidence{"one": historyEvidence(t, base, "one", "pending")}}
			s.onEvidence = func(id string) {
				switch mutation {
				case "start_before_read":
					s.evidence[id] = historyEvidence(t, base, id, "running")
				case "cancel_before_read":
					s.evidence[id] = historyEvidence(t, base, id, "cancelled")
				case "missing_attempt":
					delete(s.evidence, id)
				}
			}
			s.onIndex = func(n int) {
				if n != 2 {
					return
				}
				switch mutation {
				case "finish_after_read":
					s.evidence["one"] = historyEvidence(t, base, "one", "completed")
				case "new_attempt_after_read":
					s.evidence["two"] = historyEvidence(t, base, "two", "pending")
				}
			}
			h, err := ReadHistory(context.Background(), s, base.Registration.ID)
			if !errors.Is(err, ErrHistoryChanged) || !reflect.DeepEqual(h, History{}) {
				t.Fatal("mixed or partial history returned", h, err)
			}
		})
	}
}
