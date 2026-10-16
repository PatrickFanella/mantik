// Package researchhistory joins native condition assessments and caller-reported
// external attempts without granting execution or qualification authority.
package researchhistory

import (
	"context"
	"errors"
	"reflect"

	"git.subcult.tv/subculture-collective/mantik/internal/assessment"
	"git.subcult.tv/subculture-collective/mantik/internal/trial"
	"git.subcult.tv/subculture-collective/mantik/internal/workertrial"
)

const TrialSchema = "mantik-trial-research-history-v1"
const QuestionSchema = "mantik-hypothesis-research-history-v1"
const Authority = "research_process_history_only"

type Reader interface {
	assessment.QuestionReader
	ExternalTrialHistory(context.Context, string) (workertrial.History, error)
}
type TrialHistory struct {
	Schema              string              `json:"schema"`
	Native              assessment.History  `json:"native"`
	External            workertrial.History `json:"external"`
	Authority           string              `json:"authority"`
	QualificationCredit bool                `json:"qualification_credit"`
	SHA256              string              `json:"sha256"`
}

func validateJoin(n assessment.History, e workertrial.History) error {
	if n.Validate() != nil || e.Validate() != nil || n.Index.TrialID != e.TrialID || n.Index.HypothesisID != e.HypothesisID || n.Index.RegistrationSHA256 != e.RegistrationSHA256 {
		return trial.ErrInvalid
	}
	byID := map[string]assessment.Result{}
	for _, a := range n.Assessments {
		byID[a.AttemptID] = a
	}
	for _, j := range e.Jobs {
		run := j.NativeRun
		a, ok := byID[run.AttemptID]
		if !ok || a.RunSHA256 != run.SHA256 || a.NativeResultSHA256 != run.ResultSHA256 || a.IntentSHA256 != run.IntentSHA256 || run.DeadlineMet && a.Status != "compared" || !run.DeadlineMet && (a.Status != "not_evaluable" || a.ReasonCode != "run_completed_after_deadline") {
			return trial.ErrInvalid
		}
	}
	return nil
}
func (h TrialHistory) Validate() error {
	expected := h.SHA256
	h.SHA256 = ""
	if h.Schema != TrialSchema || h.Authority != Authority || h.QualificationCredit || validateJoin(h.Native, h.External) != nil || expected != trial.Hash(h) {
		return trial.ErrInvalid
	}
	return nil
}
func ReadTrial(ctx context.Context, r Reader, id string) (TrialHistory, error) {
	e, err := r.ExternalTrialHistory(ctx, id)
	if err != nil {
		return TrialHistory{}, err
	}
	n, err := assessment.ReadHistory(ctx, r, id)
	if err != nil {
		return TrialHistory{}, err
	}
	h := TrialHistory{Schema: TrialSchema, Native: n, External: e, Authority: Authority}
	h.SHA256 = trial.Hash(h)
	if h.Validate() != nil {
		return TrialHistory{}, trial.ErrInvalid
	}
	if err = checkExternal(ctx, r, e); err != nil {
		return TrialHistory{}, err
	}
	if err = checkNative(ctx, r, n); err != nil {
		return TrialHistory{}, err
	}
	if err = ctx.Err(); err != nil {
		return TrialHistory{}, err
	}
	return h, nil
}
func checkExternal(ctx context.Context, r Reader, e workertrial.History) error {
	latest, err := r.ExternalTrialHistory(ctx, e.TrialID)
	if errors.Is(err, trial.ErrNotFound) {
		return assessment.ErrHistoryChanged
	}
	if err != nil {
		return err
	}
	if latest.Validate() != nil {
		return trial.ErrInvalid
	}
	if latest.SHA256 != e.SHA256 {
		return assessment.ErrHistoryChanged
	}
	return nil
}
func checkNative(ctx context.Context, r Reader, n assessment.History) error {
	latest, err := r.TrialEvidenceIndex(ctx, n.Index.TrialID)
	if errors.Is(err, trial.ErrNotFound) {
		return assessment.ErrHistoryChanged
	}
	if err != nil {
		return err
	}
	if latest.Validate() != nil {
		return trial.ErrInvalid
	}
	if latest.SHA256 != n.Index.SHA256 {
		return assessment.ErrHistoryChanged
	}
	return nil
}

type QuestionHistory struct {
	Schema                  string                     `json:"schema"`
	Native                  assessment.QuestionHistory `json:"native"`
	External                []workertrial.History      `json:"external"`
	ExternalJobCount        int                        `json:"external_job_count"`
	UnattemptedExternalJobs int                        `json:"unattempted_external_jobs"`
	ExternalAttemptCount    int                        `json:"external_attempt_count"`
	PendingExternal         int                        `json:"pending_external"`
	ReportedExternal        int                        `json:"reported_external"`
	ReportedStatuses        map[string]int             `json:"reported_statuses"`
	Authority               string                     `json:"authority"`
	QualificationCredit     bool                       `json:"qualification_credit"`
	SHA256                  string                     `json:"sha256"`
}

func (q QuestionHistory) Validate() error {
	if q.Schema != QuestionSchema || q.Native.Validate() != nil || q.External == nil || len(q.External) != len(q.Native.Plans) || q.Authority != Authority || q.QualificationCredit || q.ReportedStatuses == nil {
		return trial.ErrInvalid
	}
	jobs, empty, total, pending, reported := 0, 0, 0, 0, 0
	statuses := map[string]int{}
	seenJobs, seenAttempts := map[string]bool{}, map[string]bool{}
	receiver := ""
	for i, e := range q.External {
		if validateJoin(q.Native.Plans[i].History, e) != nil || receiver != "" && receiver != e.ReceiverID {
			return trial.ErrInvalid
		}
		receiver = e.ReceiverID
		jobs += e.JobCount
		empty += e.UnattemptedJobs
		total += e.AttemptCount
		pending += e.Pending
		reported += e.Reported
		for key, n := range e.ReportedStatuses {
			statuses[key] += n
		}
		for _, j := range e.Jobs {
			if seenJobs[j.Declaration.Command.RequestID] {
				return trial.ErrInvalid
			}
			seenJobs[j.Declaration.Command.RequestID] = true
			for _, a := range j.Attempts {
				if seenAttempts[a.Intent.Command.RequestID] {
					return trial.ErrInvalid
				}
				seenAttempts[a.Intent.Command.RequestID] = true
			}
		}
	}
	if jobs > workertrial.MaxJobs || total > workertrial.MaxAttempts || jobs != q.ExternalJobCount || empty != q.UnattemptedExternalJobs || total != q.ExternalAttemptCount || pending != q.PendingExternal || reported != q.ReportedExternal || !reflect.DeepEqual(statuses, q.ReportedStatuses) {
		return trial.ErrInvalid
	}
	expected := q.SHA256
	q.SHA256 = ""
	if expected != trial.Hash(q) {
		return trial.ErrInvalid
	}
	return nil
}
func ReadQuestion(ctx context.Context, r Reader, id string) (QuestionHistory, error) {
	n, err := assessment.ReadQuestionHistory(ctx, r, id)
	if err != nil {
		return QuestionHistory{}, err
	}
	q := QuestionHistory{Schema: QuestionSchema, Native: n, External: []workertrial.History{}, ReportedStatuses: map[string]int{}, Authority: Authority}
	for _, p := range n.Plans {
		if err = ctx.Err(); err != nil {
			return QuestionHistory{}, err
		}
		e, err := r.ExternalTrialHistory(ctx, p.Registration.ID)
		if err != nil {
			return QuestionHistory{}, err
		}
		q.External = append(q.External, e)
		q.ExternalJobCount += e.JobCount
		q.UnattemptedExternalJobs += e.UnattemptedJobs
		q.ExternalAttemptCount += e.AttemptCount
		q.PendingExternal += e.Pending
		q.ReportedExternal += e.Reported
		for status, count := range e.ReportedStatuses {
			q.ReportedStatuses[status] += count
		}
	}
	q.SHA256 = trial.Hash(q)
	if q.Validate() != nil {
		return QuestionHistory{}, trial.ErrInvalid
	}
	// Append-only catalogue membership and immutable receipt hashes make these
	// final rechecks detect additions or completions anywhere in the response.
	for i, e := range q.External {
		if err = checkExternal(ctx, r, e); err != nil {
			return QuestionHistory{}, err
		}
		if err = checkNative(ctx, r, n.Plans[i].History); err != nil {
			return QuestionHistory{}, err
		}
	}
	latest, err := r.HypothesisTrials(ctx, id)
	if err != nil {
		return QuestionHistory{}, err
	}
	if trial.Hash(latest) != n.CatalogueSHA256 {
		return QuestionHistory{}, assessment.ErrHistoryChanged
	}
	if err = ctx.Err(); err != nil {
		return QuestionHistory{}, err
	}
	return q, nil
}
