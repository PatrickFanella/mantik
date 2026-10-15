package assessment

import (
	"context"
	"errors"

	"git.subcult.tv/subculture-collective/mantik/internal/trial"
)

const HistorySchema = "mantik-trial-assessment-history-v1"

var ErrHistoryChanged = errors.New("trial assessment history changed during read; retry the whole request")

type HistoryReader interface {
	TrialEvidenceIndex(context.Context, string) (trial.EvidenceIndex, error)
	TrialEvidence(context.Context, string) (trial.Evidence, error)
}
type History struct {
	Schema              string              `json:"schema"`
	Index               trial.EvidenceIndex `json:"index"`
	AttemptCount        int                 `json:"attempt_count"`
	Compared            int                 `json:"compared"`
	NotEvaluable        int                 `json:"not_evaluable"`
	Reasons             map[string]int      `json:"reasons"`
	Assessments         []Result            `json:"assessments"`
	Authority           string              `json:"authority"`
	QualificationCredit bool                `json:"qualification_credit"`
	SHA256              string              `json:"sha256"`
}

func (h History) Validate() error {
	if h.Schema != HistorySchema || h.Index.Validate() != nil || h.Authority != "synthetic_condition_comparison_only" || h.QualificationCredit || h.Assessments == nil || h.Reasons == nil || len(h.Assessments) != len(h.Index.Attempts) || h.AttemptCount != len(h.Assessments) {
		return trial.ErrInvalid
	}
	compared, notEvaluable := 0, 0
	reasons := map[string]int{}
	for i, r := range h.Assessments {
		ref := h.Index.Attempts[i]
		expected := r.SHA256
		r.SHA256 = ""
		if r.Schema != Schema || expected != trial.Hash(r) || r.AttemptID != ref.AttemptID || r.AttemptSHA256 != ref.AttemptSHA256 || r.RunSHA256 != ref.RunSHA256 || r.RegistrationSHA256 != h.Index.RegistrationSHA256 || r.HypothesisID != h.Index.HypothesisID || r.Authority != h.Authority || r.QualificationCredit {
			return trial.ErrInvalid
		}
		switch r.Status {
		case "compared":
			compared++
		case "not_evaluable":
			notEvaluable++
		default:
			return trial.ErrInvalid
		}
		reasons[r.ReasonCode]++
	}
	if compared != h.Compared || notEvaluable != h.NotEvaluable || len(reasons) != len(h.Reasons) {
		return trial.ErrInvalid
	}
	for key, n := range reasons {
		if h.Reasons[key] != n {
			return trial.ErrInvalid
		}
	}
	expected := h.SHA256
	h.SHA256 = ""
	if expected != trial.Hash(h) {
		return trial.ErrInvalid
	}
	return nil
}

// ReadHistory verifies a stable metadata boundary before returning any output.
// At most one bounded native result body is loaded at a time. No retry, worker
// liveness inference, execution or winner selection occurs here.
func ReadHistory(ctx context.Context, reader HistoryReader, id string) (History, error) {
	index, err := reader.TrialEvidenceIndex(ctx, id)
	if err != nil {
		return History{}, err
	}
	if index.Validate() != nil || index.TrialID != id {
		return History{}, trial.ErrInvalid
	}
	return readHistoryAtIndex(ctx, reader, index)
}

func readHistoryAtIndex(ctx context.Context, reader HistoryReader, index trial.EvidenceIndex) (History, error) {
	var err error
	h := History{Schema: HistorySchema, Index: index, AttemptCount: len(index.Attempts), Reasons: map[string]int{}, Assessments: []Result{}, Authority: "synthetic_condition_comparison_only"}
	for _, ref := range index.Attempts {
		if err = ctx.Err(); err != nil {
			return History{}, err
		}
		e, err := reader.TrialEvidence(ctx, ref.AttemptID)
		if errors.Is(err, trial.ErrNotFound) {
			return History{}, ErrHistoryChanged
		}
		if err != nil {
			return History{}, err
		}
		runSHA := ""
		if e.Run != nil {
			runSHA = e.Run.SHA256
		}
		if e.Attempt.ID != ref.AttemptID || e.Attempt.SHA256 != ref.AttemptSHA256 || !e.Attempt.StartedAt.Equal(ref.StartedAt) || runSHA != ref.RunSHA256 || e.Registration.ID != index.TrialID || e.Registration.SHA256 != index.RegistrationSHA256 {
			return History{}, ErrHistoryChanged
		}
		r, err := Build(e)
		if err != nil {
			return History{}, err
		}
		h.Assessments = append(h.Assessments, r)
		h.Reasons[r.ReasonCode]++
		if r.Status == "compared" {
			h.Compared++
		} else {
			h.NotEvaluable++
		}
	}
	// Catch changed/cancelled/finished attempts and additions anywhere in the
	// catalogue, including after their individual evidence was read.
	latest, err := reader.TrialEvidenceIndex(ctx, index.TrialID)
	if err != nil {
		return History{}, err
	}
	if latest.Validate() != nil {
		return History{}, trial.ErrInvalid
	}
	if latest.SHA256 != index.SHA256 {
		return History{}, ErrHistoryChanged
	}
	if err = ctx.Err(); err != nil {
		return History{}, err
	}
	h.SHA256 = trial.Hash(h)
	return h, h.Validate()
}
