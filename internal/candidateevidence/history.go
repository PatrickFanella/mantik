package candidateevidence

import (
	"encoding/json"
	"git.subcult.tv/subculture-collective/mantik/internal/candidateattempt"
	"git.subcult.tv/subculture-collective/mantik/internal/workerplan"
	"slices"
)

const HistorySchema = "mantik-candidate-worker-outcome-history-v1"
const MaxHistoryBytes = candidateattempt.MaxHistoryBytes + candidateattempt.MaxAttempts*(MaxOutcomeBytes+256) + 4096

// History is a single backend snapshot of declarations and retained reports.
// UnreportedRequestIDs says only that this snapshot contains no retained report
// for those intents. It cannot establish whether a worker is running or absent.
type History struct {
	Schema                string                   `json:"schema"`
	ReceiverID            string                   `json:"receiver_id"`
	IntentHistory         candidateattempt.History `json:"intent_history"`
	Outcomes              []OutcomeRecord          `json:"outcomes"`
	UnreportedRequestIDs  []string                 `json:"unreported_request_ids"`
	Authority             string                   `json:"authority"`
	ExecutionVerified     bool                     `json:"execution_verified"`
	CurrentHostStateKnown bool                     `json:"current_host_state_known"`
	QualificationCredit   bool                     `json:"qualification_credit"`
	SHA256                string                   `json:"sha256"`
}

// HistoryFor requires the backend to validate the full captures and authoritative
// registry/dataset bindings in the same snapshot before constructing this view.
func HistoryFor(intents candidateattempt.History, outcomes []OutcomeRecord) (History, error) {
	h := History{Schema: HistorySchema, ReceiverID: intents.ReceiverID, IntentHistory: intents, Outcomes: append([]OutcomeRecord{}, outcomes...), UnreportedRequestIDs: []string{}, Authority: "retained_candidate_worker_reports_only"}
	reported := map[string]bool{}
	for _, r := range outcomes {
		reported[r.RequestID] = true
	}
	for _, i := range intents.Intents {
		if !reported[i.Command.RequestID] {
			h.UnreportedRequestIDs = append(h.UnreportedRequestIDs, i.Command.RequestID)
		}
	}
	raw, err := json.Marshal(h)
	if err != nil {
		return History{}, ErrConflict
	}
	var detached History
	if err := json.Unmarshal(raw, &detached); err != nil {
		return History{}, ErrConflict
	}
	detached.SHA256 = workerplan.Hash(detached)
	return detached, detached.Validate()
}
func (h History) Validate() error {
	if h.Schema != HistorySchema || h.IntentHistory.Validate() != nil || h.ReceiverID != h.IntentHistory.ReceiverID || h.Outcomes == nil || len(h.Outcomes) > candidateattempt.MaxAttempts || h.UnreportedRequestIDs == nil || h.Authority != "retained_candidate_worker_reports_only" || h.ExecutionVerified || h.CurrentHostStateKnown || h.QualificationCredit {
		return ErrConflict
	}
	last := 0
	reported := map[string]bool{}
	for _, r := range h.Outcomes {
		if r.Validate() != nil || r.ReceiverID != h.ReceiverID || r.IntentSequence <= last || r.IntentSequence > len(h.IntentHistory.Intents) {
			return ErrConflict
		}
		i := h.IntentHistory.Intents[r.IntentSequence-1]
		if r.RequestID != i.Command.RequestID || r.IntentSHA256 != i.SHA256 || r.JobSHA256 != i.Command.Job.SHA256 || r.RecordedAt.Before(i.CreatedAt) {
			return ErrConflict
		}
		last = r.IntentSequence
		reported[r.RequestID] = true
	}
	unreported := []string{}
	for _, i := range h.IntentHistory.Intents {
		if !reported[i.Command.RequestID] {
			unreported = append(unreported, i.Command.RequestID)
		}
	}
	if !slices.Equal(unreported, h.UnreportedRequestIDs) {
		return ErrConflict
	}
	sha := h.SHA256
	h.SHA256 = ""
	if sha != workerplan.Hash(h) {
		return ErrConflict
	}
	return nil
}
