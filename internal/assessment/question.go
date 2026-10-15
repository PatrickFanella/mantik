package assessment

import (
	"context"
	"errors"
	"reflect"
	"regexp"
	"strings"

	"git.subcult.tv/subculture-collective/mantik/internal/hypothesis"
	"git.subcult.tv/subculture-collective/mantik/internal/review"
	"git.subcult.tv/subculture-collective/mantik/internal/trial"
)

const QuestionSchema = "mantik-hypothesis-assessment-history-v1"

var receiptDigest = regexp.MustCompile(`^[a-f0-9]{64}$`)

type QuestionReader interface {
	HistoryReader
	HypothesisTrials(context.Context, string) (trial.History, error)
	Trial(context.Context, string) (trial.Registration, error)
}
type PlanHistory struct {
	Registration      trial.Summary `json:"registration"`
	PublicationSHA256 string        `json:"publication_sha256"`
	ParentSHA256      string        `json:"parent_sha256,omitempty"`
	History           History       `json:"history"`
}
type QuestionHistory struct {
	Schema              string         `json:"schema"`
	HypothesisID        string         `json:"hypothesis_id"`
	CatalogueSHA256     string         `json:"catalogue_sha256"`
	PlanCount           int            `json:"plan_count"`
	NeverAttemptedPlans int            `json:"never_attempted_plans"`
	AttemptCount        int            `json:"attempt_count"`
	Compared            int            `json:"compared"`
	NotEvaluable        int            `json:"not_evaluable"`
	Reasons             map[string]int `json:"reasons"`
	Plans               []PlanHistory  `json:"plans"`
	Authority           string         `json:"authority"`
	QualificationCredit bool           `json:"qualification_credit"`
	SHA256              string         `json:"sha256"`
}

func validCatalogue(c trial.History, id string) bool {
	if c.Schema != trial.HistorySchema || c.HypothesisID != id || !hypothesis.QuestionID.MatchString(id) || c.Authority != "research_trial_declaration_only" || c.QualificationCredit || c.Registrations == nil || len(c.Registrations) > trial.MaxRegistrations {
		return false
	}
	seen := map[string]bool{}
	for i, r := range c.Registrations {
		if !review.ID.MatchString(r.ID) || !review.ID.MatchString(r.PublicationID) || !receiptDigest.MatchString(r.DeclarationSHA256) || !receiptDigest.MatchString(r.RecipeSHA256) || !receiptDigest.MatchString(r.SHA256) || r.Status != "registered" || r.RegisteredAt.IsZero() || r.RegisteredAt.UTC().Year() < 1 || r.RegisteredAt.UTC().Year() > 9999 || seen[r.ID] || r.ParentID != "" && (!review.ID.MatchString(r.ParentID) || r.ParentID == r.ID) {
			return false
		}
		if i > 0 {
			prev := c.Registrations[i-1]
			if prev.RegisteredAt.After(r.RegisteredAt) || prev.RegisteredAt.Equal(r.RegisteredAt) && strings.Compare(prev.ID, r.ID) >= 0 {
				return false
			}
		}
		seen[r.ID] = true
	}
	return true
}

func (q QuestionHistory) Validate() error {
	if q.Schema != QuestionSchema || !hypothesis.QuestionID.MatchString(q.HypothesisID) || q.Authority != "synthetic_condition_comparison_only" || q.QualificationCredit || q.Plans == nil || q.Reasons == nil || q.PlanCount != len(q.Plans) || len(q.Plans) > trial.MaxRegistrations {
		return trial.ErrInvalid
	}
	c := trial.History{Schema: trial.HistorySchema, HypothesisID: q.HypothesisID, Authority: "research_trial_declaration_only", Registrations: []trial.Summary{}}
	byID := map[string]PlanHistory{}
	attempts := map[string]bool{}
	reasons := map[string]int{}
	total, compared, notEvaluable, empty := 0, 0, 0, 0
	for _, p := range q.Plans {
		r, h := p.Registration, p.History
		if !receiptDigest.MatchString(p.PublicationSHA256) || r.ParentID == "" && p.ParentSHA256 != "" || r.ParentID != "" && !receiptDigest.MatchString(p.ParentSHA256) || h.Validate() != nil || h.Index.TrialID != r.ID || h.Index.HypothesisID != q.HypothesisID || h.Index.RegistrationSHA256 != r.SHA256 {
			return trial.ErrInvalid
		}
		for _, a := range h.Assessments {
			if attempts[a.AttemptID] || a.PublicationSHA256 != p.PublicationSHA256 || a.DeclarationSHA256 != r.DeclarationSHA256 {
				return trial.ErrInvalid
			}
			attempts[a.AttemptID] = true
		}
		c.Registrations = append(c.Registrations, r)
		byID[r.ID] = p
		total += h.AttemptCount
		compared += h.Compared
		notEvaluable += h.NotEvaluable
		if h.AttemptCount == 0 {
			empty++
		}
		for reason, n := range h.Reasons {
			reasons[reason] += n
		}
	}
	if !validCatalogue(c, q.HypothesisID) || trial.Hash(c) != q.CatalogueSHA256 || total > trial.MaxAttempts || total != q.AttemptCount || compared != q.Compared || notEvaluable != q.NotEvaluable || empty != q.NeverAttemptedPlans || !reflect.DeepEqual(reasons, q.Reasons) {
		return trial.ErrInvalid
	}
	// Verify every parent receipt, ordering and complete ancestry, even when a
	// plan has no attempts. Equal registration times do not excuse cycles.
	for _, p := range q.Plans {
		seen := map[string]bool{p.Registration.ID: true}
		current := p
		for current.Registration.ParentID != "" {
			parent, ok := byID[current.Registration.ParentID]
			if !ok || seen[parent.Registration.ID] || parent.Registration.SHA256 != current.ParentSHA256 || current.Registration.RegisteredAt.Before(parent.Registration.RegisteredAt) {
				return trial.ErrInvalid
			}
			seen[parent.Registration.ID] = true
			current = parent
		}
	}
	expected := q.SHA256
	q.SHA256 = ""
	if expected != trial.Hash(q) {
		return trial.ErrInvalid
	}
	return nil
}

// ReadQuestionHistory pins every plan and attempt catalogue before loading any
// result bodies, then rechecks all catalogues before returning compact output.
func ReadQuestionHistory(ctx context.Context, reader QuestionReader, id string) (QuestionHistory, error) {
	catalogue, err := reader.HypothesisTrials(ctx, id)
	if err != nil {
		return QuestionHistory{}, err
	}
	if !validCatalogue(catalogue, id) {
		return QuestionHistory{}, trial.ErrInvalid
	}
	q := QuestionHistory{Schema: QuestionSchema, HypothesisID: id, CatalogueSHA256: trial.Hash(catalogue), PlanCount: len(catalogue.Registrations), Reasons: map[string]int{}, Plans: []PlanHistory{}, Authority: "synthetic_condition_comparison_only"}
	indices := []trial.EvidenceIndex{}
	receiver := ""
	total := 0
	for _, summary := range catalogue.Registrations {
		if err = ctx.Err(); err != nil {
			return QuestionHistory{}, err
		}
		r, err := reader.Trial(ctx, summary.ID)
		if errors.Is(err, trial.ErrNotFound) {
			return QuestionHistory{}, ErrHistoryChanged
		}
		if err != nil {
			return QuestionHistory{}, err
		}
		if r.Validate() != nil || r.HypothesisID != id || !reflect.DeepEqual(trial.Summarize(r), summary) || receiver != "" && receiver != r.ReceiverID {
			return QuestionHistory{}, trial.ErrInvalid
		}
		receiver = r.ReceiverID
		index, err := reader.TrialEvidenceIndex(ctx, r.ID)
		if err != nil {
			return QuestionHistory{}, err
		}
		if index.Validate() != nil || index.TrialID != r.ID || index.HypothesisID != id || index.RegistrationSHA256 != r.SHA256 {
			return QuestionHistory{}, trial.ErrInvalid
		}
		total += len(index.Attempts)
		if total > trial.MaxAttempts {
			return QuestionHistory{}, trial.ErrInvalid
		}
		indices = append(indices, index)
		q.Plans = append(q.Plans, PlanHistory{Registration: summary, PublicationSHA256: r.PublicationSHA256, ParentSHA256: r.ParentSHA256})
	}
	for i, index := range indices {
		h, err := readHistoryAtIndex(ctx, reader, index)
		if err != nil {
			return QuestionHistory{}, err
		}
		q.Plans[i].History = h
		q.AttemptCount += h.AttemptCount
		q.Compared += h.Compared
		q.NotEvaluable += h.NotEvaluable
		if h.AttemptCount == 0 {
			q.NeverAttemptedPlans++
		}
		for reason, n := range h.Reasons {
			q.Reasons[reason] += n
		}
	}
	latest, err := reader.HypothesisTrials(ctx, id)
	if err != nil {
		return QuestionHistory{}, err
	}
	if !validCatalogue(latest, id) {
		return QuestionHistory{}, trial.ErrInvalid
	}
	if trial.Hash(latest) != q.CatalogueSHA256 {
		return QuestionHistory{}, ErrHistoryChanged
	}
	for _, index := range indices {
		latest, err := reader.TrialEvidenceIndex(ctx, index.TrialID)
		if err != nil {
			return QuestionHistory{}, err
		}
		if latest.Validate() != nil {
			return QuestionHistory{}, trial.ErrInvalid
		}
		if latest.SHA256 != index.SHA256 {
			return QuestionHistory{}, ErrHistoryChanged
		}
	}
	if err = ctx.Err(); err != nil {
		return QuestionHistory{}, err
	}
	q.SHA256 = trial.Hash(q)
	if q.Validate() != nil {
		return QuestionHistory{}, trial.ErrInvalid
	}
	return q, nil
}
