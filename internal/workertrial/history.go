package workertrial

import (
	"reflect"
	"sort"

	"git.subcult.tv/subculture-collective/mantik/internal/hypothesis"
	"git.subcult.tv/subculture-collective/mantik/internal/review"
	"git.subcult.tv/subculture-collective/mantik/internal/trial"
)

const HistorySchema = "mantik-external-trial-history-v1"

type JobHistory struct {
	Declaration Record    `json:"declaration"`
	NativeRun   trial.Run `json:"native_run"`
	Attempts    []Attempt `json:"attempts"`
}
type History struct {
	Schema              string         `json:"schema"`
	ReceiverID          string         `json:"receiver_id"`
	TrialID             string         `json:"trial_id"`
	HypothesisID        string         `json:"hypothesis_id"`
	RegistrationSHA256  string         `json:"registration_sha256"`
	Jobs                []JobHistory   `json:"jobs"`
	JobCount            int            `json:"job_count"`
	UnattemptedJobs     int            `json:"unattempted_jobs"`
	AttemptCount        int            `json:"attempt_count"`
	Pending             int            `json:"pending"`
	Reported            int            `json:"reported"`
	ReportedStatuses    map[string]int `json:"reported_statuses"`
	Authority           string         `json:"authority"`
	QualificationCredit bool           `json:"qualification_credit"`
	SHA256              string         `json:"sha256"`
}

func (h History) Validate() error {
	if h.Schema != HistorySchema || !receiver.MatchString(h.ReceiverID) || !review.ID.MatchString(h.TrialID) || !hypothesis.QuestionID.MatchString(h.HypothesisID) || !hex.MatchString(h.RegistrationSHA256) || h.Jobs == nil || h.JobCount != len(h.Jobs) || len(h.Jobs) > MaxJobs || h.Authority != "external_attempt_history_only" || h.QualificationCredit || h.ReportedStatuses == nil {
		return trial.ErrInvalid
	}
	total, pending, reported, empty := 0, 0, 0, 0
	statuses := map[string]int{}
	seen := map[string]bool{}
	last := ""
	for _, job := range h.Jobs {
		r, run := job.Declaration, job.NativeRun
		c := r.Command
		if r.Validate() != nil || run.Validate() != nil || c.RequestID <= last || r.TrialID != h.TrialID || r.HypothesisID != h.HypothesisID || r.RegistrationSHA256 != h.RegistrationSHA256 || c.ReceiverID != h.ReceiverID || run.Status != "completed" || run.AttemptID != c.AttemptID || run.SHA256 != c.NativeRunSHA256 || run.ReceiverID != h.ReceiverID || run.TrialID != h.TrialID || run.RegistrationSHA256 != h.RegistrationSHA256 || run.RecipeSHA256 != r.Job.Recipe.SHA256 || run.DeadlineMet != r.NativeDeadlineMet || r.DeclaredAt.Before(run.FinishedAt) || r.Job.NativeReference.ID != run.NativeAttemptID || r.Job.Inputs[0].SHA256 != run.InputSHA256 || r.Job.Inputs[2].SHA256 != run.ResultSHA256 || r.Job.Inputs[2].Bytes != run.ResultBytes || job.Attempts == nil {
			return trial.ErrInvalid
		}
		last = c.RequestID
		if len(job.Attempts) == 0 {
			empty++
		}
		lastAttempt := ""
		for _, a := range job.Attempts {
			i := a.Intent
			if a.Validate() != nil || i.Command.RequestID <= lastAttempt || seen[i.Command.RequestID] || i.Command.JobID != c.RequestID || i.Command.ReceiverID != h.ReceiverID || i.Command.DeclarationSHA256 != r.SHA256 || i.JobSHA256 != r.Job.SHA256 || i.CreatedAt.Before(r.DeclaredAt) {
				return trial.ErrInvalid
			}
			lastAttempt = i.Command.RequestID
			seen[lastAttempt] = true
			total++
			if a.Outcome == nil {
				pending++
			} else {
				reported++
				statuses[a.Outcome.ReportedStatus]++
			}
		}
	}
	if total > MaxAttempts || total != h.AttemptCount || pending != h.Pending || reported != h.Reported || empty != h.UnattemptedJobs || !reflect.DeepEqual(statuses, h.ReportedStatuses) {
		return trial.ErrInvalid
	}
	expected := h.SHA256
	h.SHA256 = ""
	if expected != trial.Hash(h) {
		return trial.ErrInvalid
	}
	return nil
}
func MakeHistory(r trial.Registration, jobs []Record, runs []trial.Run, attempts []Attempt) (History, error) {
	if r.Validate() != nil || len(jobs) > MaxJobs || len(runs) != len(jobs) || len(attempts) > MaxAttempts {
		return History{}, trial.ErrInvalid
	}
	byRun := map[string]trial.Run{}
	for _, run := range runs {
		if old, ok := byRun[run.AttemptID]; ok && !reflect.DeepEqual(old, run) {
			return History{}, trial.ErrInvalid
		}
		byRun[run.AttemptID] = run
	}
	usedRuns := map[string]bool{}
	byJob := map[string][]Attempt{}
	for _, a := range attempts {
		byJob[a.Intent.Command.JobID] = append(byJob[a.Intent.Command.JobID], a)
	}
	h := History{Schema: HistorySchema, ReceiverID: r.ReceiverID, TrialID: r.ID, HypothesisID: r.HypothesisID, RegistrationSHA256: r.SHA256, Jobs: []JobHistory{}, ReportedStatuses: map[string]int{}, Authority: "external_attempt_history_only"}
	for _, j := range jobs {
		usedRuns[j.Command.AttemptID] = true
		list := byJob[j.Command.RequestID]
		delete(byJob, j.Command.RequestID)
		if list == nil {
			list = []Attempt{}
		}
		sort.Slice(list, func(i, j int) bool { return list[i].Intent.Command.RequestID < list[j].Intent.Command.RequestID })
		h.Jobs = append(h.Jobs, JobHistory{j, byRun[j.Command.AttemptID], list})
		h.JobCount++
		h.AttemptCount += len(list)
		if len(list) == 0 {
			h.UnattemptedJobs++
		}
		for _, a := range list {
			if a.Outcome == nil {
				h.Pending++
			} else {
				h.Reported++
				h.ReportedStatuses[a.Outcome.ReportedStatus]++
			}
		}
	}
	if len(byJob) != 0 || len(usedRuns) != len(byRun) {
		return History{}, trial.ErrInvalid
	}
	sort.Slice(h.Jobs, func(i, j int) bool {
		return h.Jobs[i].Declaration.Command.RequestID < h.Jobs[j].Declaration.Command.RequestID
	})
	h.SHA256 = trial.Hash(h)
	return h, h.Validate()
}
