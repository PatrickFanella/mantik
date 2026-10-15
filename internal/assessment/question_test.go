package assessment

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/hypothesis"
	"git.subcult.tv/subculture-collective/mantik/internal/trial"
)

type questionReader struct {
	id             string
	registrations  map[string]trial.Registration
	plans          map[string]*historyReader
	catalogueReads int
	onCatalogue    func(int)
}

func (s *questionReader) HypothesisTrials(ctx context.Context, id string) (trial.History, error) {
	if err := ctx.Err(); err != nil {
		return trial.History{}, err
	}
	if id != s.id {
		return trial.History{}, trial.ErrNotFound
	}
	s.catalogueReads++
	if s.onCatalogue != nil {
		s.onCatalogue(s.catalogueReads)
	}
	h := trial.History{Schema: trial.HistorySchema, HypothesisID: id, Authority: "research_trial_declaration_only", Registrations: []trial.Summary{}}
	for _, r := range s.registrations {
		h.Registrations = append(h.Registrations, trial.Summarize(r))
	}
	slices.SortFunc(h.Registrations, func(a, b trial.Summary) int {
		if c := a.RegisteredAt.Compare(b.RegisteredAt); c != 0 {
			return c
		}
		return strings.Compare(a.ID, b.ID)
	})
	return h, nil
}
func (s *questionReader) Trial(ctx context.Context, id string) (trial.Registration, error) {
	if err := ctx.Err(); err != nil {
		return trial.Registration{}, err
	}
	r, ok := s.registrations[id]
	if !ok {
		return r, trial.ErrNotFound
	}
	return r, nil
}
func (s *questionReader) TrialEvidenceIndex(ctx context.Context, id string) (trial.EvidenceIndex, error) {
	p, ok := s.plans[id]
	if !ok {
		return trial.EvidenceIndex{}, trial.ErrNotFound
	}
	return p.TrialEvidenceIndex(ctx, id)
}
func (s *questionReader) TrialEvidence(ctx context.Context, id string) (trial.Evidence, error) {
	for _, p := range s.plans {
		if _, ok := p.evidence[id]; ok {
			return p.TrialEvidence(ctx, id)
		}
	}
	return trial.Evidence{}, trial.ErrNotFound
}
func addQuestionPlan(t *testing.T, s *questionReader, base trial.Evidence, id, parentID string, offset time.Duration) trial.Evidence {
	t.Helper()
	var parent *trial.Registration
	if parentID != "" {
		r := s.registrations[parentID]
		parent = &r
	}
	r, err := trial.Register(trial.Command{ReceiverID: base.Registration.ReceiverID, RequestID: id, PublicationID: base.Publication.ID, ParentID: parentID, Recipe: base.Registration.Recipe}, base.Publication, parent, base.Registration.RegisteredAt.Add(offset))
	if err != nil {
		t.Fatal(err)
	}
	s.registrations[id] = r
	s.plans[id] = &historyReader{registration: r, evidence: map[string]trial.Evidence{}}
	base.Registration = r
	base.Attempt.StartedAt = r.RegisteredAt
	return base
}
func questionFixture(t *testing.T) (*questionReader, trial.Evidence) {
	t.Helper()
	base := fixture(t)
	s := &questionReader{id: base.Registration.HypothesisID, registrations: map[string]trial.Registration{}, plans: map[string]*historyReader{}}
	root := addQuestionPlan(t, s, base, "root", "", 0)
	child := addQuestionPlan(t, s, base, "child", "root", time.Second)
	alternative := base
	alternative.Publication.ID = "alternative-publication"
	d := alternative.Publication.Sealed.Declaration
	d.Label = "same-question-another-publication"
	var err error
	alternative.Publication.Sealed, err = hypothesis.Seal(d)
	if err != nil {
		t.Fatal(err)
	}
	sibling := addQuestionPlan(t, s, alternative, "sibling", "root", 2*time.Second)
	addQuestionPlan(t, s, base, "empty", "child", 3*time.Second)
	for _, entry := range []struct {
		base       trial.Evidence
		id, status string
	}{
		{root, "completed", "completed"}, {root, "failed", "failed"},
		{child, "running", "running"}, {child, "incomplete", "incomplete"},
		{sibling, "cancelled", "cancelled"}, {sibling, "pending", "pending"},
	} {
		s.plans[entry.base.Registration.ID].evidence[entry.id] = historyEvidence(t, entry.base, entry.id, entry.status)
	}
	return s, base
}
func TestQuestionHistoryPreservesAllPlansAndAttempts(t *testing.T) {
	s, base := questionFixture(t)
	q, err := ReadQuestionHistory(context.Background(), s, s.id)
	if err != nil || q.Validate() != nil || q.PlanCount != 4 || q.NeverAttemptedPlans != 1 || q.AttemptCount != 6 || q.Compared != 1 || q.NotEvaluable != 5 || len(q.Reasons) != 6 || q.QualificationCredit {
		t.Fatal(q, err)
	}
	if q.Plans[1].ParentSHA256 != q.Plans[0].Registration.SHA256 || q.Plans[2].ParentSHA256 != q.Plans[0].Registration.SHA256 || q.Plans[3].ParentSHA256 != q.Plans[1].Registration.SHA256 || q.Plans[3].History.AttemptCount != 0 {
		t.Fatal("lineage or zero-attempt plan lost")
	}
	if q.Plans[2].PublicationSHA256 == q.Plans[0].PublicationSHA256 || q.Plans[2].Registration.DeclarationSHA256 == q.Plans[0].Registration.DeclarationSHA256 {
		t.Fatal("distinct publication/declaration provenance collapsed")
	}
	for _, p := range q.Plans {
		want, err := ReadHistory(context.Background(), s, p.Registration.ID)
		if err != nil || !reflect.DeepEqual(p.History, want) {
			t.Fatal("question report changed plan history", err)
		}
	}
	repeated, err := ReadQuestionHistory(context.Background(), s, base.Registration.HypothesisID)
	if err != nil || !reflect.DeepEqual(q, repeated) {
		t.Fatal("unstable question report", err)
	}
}
func resealQuestion(q *QuestionHistory) {
	slices.SortFunc(q.Plans, func(a, b PlanHistory) int {
		if c := a.Registration.RegisteredAt.Compare(b.Registration.RegisteredAt); c != 0 {
			return c
		}
		return strings.Compare(a.Registration.ID, b.Registration.ID)
	})
	c := trial.History{Schema: trial.HistorySchema, HypothesisID: q.HypothesisID, Authority: "research_trial_declaration_only", Registrations: []trial.Summary{}}
	for _, p := range q.Plans {
		c.Registrations = append(c.Registrations, p.Registration)
	}
	q.CatalogueSHA256 = trial.Hash(c)
	q.SHA256 = ""
	q.SHA256 = trial.Hash(*q)
}
func TestQuestionHistoryRejectsLineageAndCounterCorruption(t *testing.T) {
	for _, change := range []func(*QuestionHistory){
		func(q *QuestionHistory) { q.Plans[1].ParentSHA256 = strings.Repeat("0", 64) },
		func(q *QuestionHistory) { q.Plans[1].Registration.ParentID = "missing" },
		func(q *QuestionHistory) {
			q.Plans[0].Registration.ParentID = "child"
			q.Plans[0].ParentSHA256 = q.Plans[1].Registration.SHA256
			q.Plans[1].Registration.RegisteredAt = q.Plans[0].Registration.RegisteredAt
		},
		func(q *QuestionHistory) {
			q.Plans[3].Registration.RegisteredAt = q.Plans[0].Registration.RegisteredAt.Add(-time.Second)
		},
		func(q *QuestionHistory) { q.Compared++ },
		func(q *QuestionHistory) { q.NeverAttemptedPlans = 0 },
		func(q *QuestionHistory) { q.Plans[0].PublicationSHA256 = strings.Repeat("0", 64) },
	} {
		s, _ := questionFixture(t)
		q, err := ReadQuestionHistory(context.Background(), s, s.id)
		if err != nil {
			t.Fatal(err)
		}
		change(&q)
		resealQuestion(&q)
		if q.Validate() == nil {
			t.Fatal("corrupt question history accepted")
		}
	}
}
func TestQuestionHistoryRejectsCrossPlanChanges(t *testing.T) {
	for _, mutation := range []string{"new_plan", "new_attempt_on_empty_plan", "change_already_read_plan"} {
		t.Run(mutation, func(t *testing.T) {
			s, base := questionFixture(t)
			s.onCatalogue = func(n int) {
				if n != 2 {
					return
				}
				switch mutation {
				case "new_plan":
					addQuestionPlan(t, s, base, "new-plan", "root", 4*time.Second)
				case "new_attempt_on_empty_plan":
					r := s.registrations["empty"]
					b := base
					b.Registration = r
					b.Attempt.StartedAt = r.RegisteredAt
					s.plans["empty"].evidence["new-intent"] = historyEvidence(t, b, "new-intent", "pending")
				case "change_already_read_plan":
					e := s.plans["sibling"].evidence["pending"]
					e.Attempt, _ = trial.CancelAttempt(e.Attempt, e.Attempt.StartedAt)
					s.plans["sibling"].evidence["pending"] = e
				}
			}
			q, err := ReadQuestionHistory(context.Background(), s, s.id)
			if !errors.Is(err, ErrHistoryChanged) || !reflect.DeepEqual(q, QuestionHistory{}) {
				t.Fatal("mixed or partial question report returned", q, err)
			}
		})
	}
}
func TestQuestionHistoryEmptyAndBounds(t *testing.T) {
	base := fixture(t)
	s := &questionReader{id: base.Registration.HypothesisID, registrations: map[string]trial.Registration{}, plans: map[string]*historyReader{}}
	q, err := ReadQuestionHistory(context.Background(), s, s.id)
	if err != nil || q.Validate() != nil || q.PlanCount != 0 || q.Plans == nil || q.AttemptCount != 0 {
		t.Fatal(q, err)
	}
	for i := range trial.MaxRegistrations {
		addQuestionPlan(t, s, base, fmt.Sprintf("plan-%02d", i), "", time.Duration(i)*time.Second)
	}
	q, err = ReadQuestionHistory(context.Background(), s, s.id)
	if err != nil || q.PlanCount != 32 || q.NeverAttemptedPlans != 32 {
		t.Fatal(q, err)
	}
	for i := range trial.MaxAttempts {
		id := fmt.Sprintf("plan-%02d", i)
		b := base
		b.Registration = s.registrations[id]
		b.Attempt.StartedAt = b.Registration.RegisteredAt
		intentID := fmt.Sprintf("intent-%02d", i)
		s.plans[id].evidence[intentID] = historyEvidence(t, b, intentID, "pending")
	}
	q, err = ReadQuestionHistory(context.Background(), s, s.id)
	if err != nil || q.AttemptCount != 32 || q.NotEvaluable != 32 || q.NeverAttemptedPlans != 0 {
		t.Fatal(q, err)
	}
	b := base
	b.Registration = s.registrations["plan-00"]
	s.plans["plan-00"].evidence["extra-intent"] = historyEvidence(t, b, "extra-intent", "pending")
	if _, err = ReadQuestionHistory(context.Background(), s, s.id); !errors.Is(err, trial.ErrInvalid) {
		t.Fatal("over-capacity attempt total accepted", err)
	}
	delete(s.plans["plan-00"].evidence, "extra-intent")
	addQuestionPlan(t, s, base, "overflow", "", time.Minute)
	if _, err = ReadQuestionHistory(context.Background(), s, s.id); !errors.Is(err, trial.ErrInvalid) {
		t.Fatal("over-capacity catalogue accepted", err)
	}
	if _, err = ReadQuestionHistory(context.Background(), s, "hypothesis-"+strings.Repeat("0", 64)); !errors.Is(err, trial.ErrNotFound) {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err = ReadQuestionHistory(ctx, s, s.id); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}
