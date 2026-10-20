package candidateattempt

import (
	"encoding/json"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/candidatelineage"
	"git.subcult.tv/subculture-collective/mantik/internal/review"
	"git.subcult.tv/subculture-collective/mantik/internal/workerplan"
)

const HistorySchema = "mantik-candidate-worker-intent-history-v1"
const MaxHistoryBytes = MaxAttempts*MaxBytes + candidatelineage.MaxRegistrations*1024 + 4096

// RegistrationPin is a server-reported link. Inspect the full registration to
// check its content hash; this summary does not independently attest ownership.
type RegistrationPin struct {
	ID              string    `json:"id"`
	Sequence        int       `json:"sequence"`
	SHA256          string    `json:"sha256"`
	CandidateSHA256 string    `json:"candidate_sha256"`
	RegisteredAt    time.Time `json:"registered_at"`
}

type History struct {
	Schema              string            `json:"schema"`
	ReceiverID          string            `json:"receiver_id"`
	Registrations       []RegistrationPin `json:"registrations"`
	Intents             []Intent          `json:"intents"`
	Authority           string            `json:"authority"`
	ExecutionVerified   bool              `json:"execution_verified"`
	HostStateKnown      bool              `json:"host_state_known"`
	QualificationCredit bool              `json:"qualification_credit"`
	SHA256              string            `json:"sha256"`
}

func historyHash(h History) string { h.SHA256 = ""; return workerplan.Hash(h) }

// HistoryFor consumes one already-validated backend snapshot. It checks all
// declaration bindings and includes pins only for referenced registrations.
func HistoryFor(receiverID string, registered []candidatelineage.Registration, all []Intent) (History, error) {
	if candidatelineage.ValidateHistory(receiverID, registered) != nil || ValidateHistory(receiverID, all) != nil {
		return History{}, ErrInvalid
	}
	regs := map[string]candidatelineage.Registration{}
	used := map[string]bool{}
	for _, r := range registered {
		regs[r.ID] = r
	}
	for _, i := range all {
		r, ok := regs[i.Command.RegistrationID]
		if !ok || i.VerifyBinding(r) != nil {
			return History{}, ErrInvalid
		}
		used[r.ID] = true
	}
	h := History{Schema: HistorySchema, ReceiverID: receiverID, Registrations: []RegistrationPin{}, Intents: []Intent{}, Authority: "retained_candidate_worker_intents_only"}
	for _, r := range registered {
		if used[r.ID] {
			h.Registrations = append(h.Registrations, RegistrationPin{ID: r.ID, Sequence: r.Sequence, SHA256: r.SHA256, CandidateSHA256: r.Candidate.SHA256, RegisteredAt: r.RegisteredAt})
		}
	}
	h.Intents = append(h.Intents, all...)
	raw, _ := json.Marshal(h)
	var detached History
	_ = json.Unmarshal(raw, &detached)
	detached.SHA256 = historyHash(detached)
	return detached, detached.Validate()
}

func (h History) Validate() error {
	if h.Schema != HistorySchema || h.Registrations == nil || len(h.Registrations) > candidatelineage.MaxRegistrations || h.Intents == nil || ValidateHistory(h.ReceiverID, h.Intents) != nil || h.Authority != "retained_candidate_worker_intents_only" || h.ExecutionVerified || h.HostStateKnown || h.QualificationCredit || h.SHA256 != historyHash(h) {
		return ErrInvalid
	}
	refs := map[string]RegistrationPin{}
	used := map[string]bool{}
	last := 0
	var lastAt time.Time
	for _, r := range h.Registrations {
		if !review.ID.MatchString(r.ID) || r.Sequence <= last || r.Sequence > candidatelineage.MaxRegistrations || !digest.MatchString(r.SHA256) || !digest.MatchString(r.CandidateSHA256) || !validTime(r.RegisteredAt) || r.RegisteredAt.Before(lastAt) {
			return ErrInvalid
		}
		if _, exists := refs[r.ID]; exists {
			return ErrInvalid
		}
		refs[r.ID] = r
		last = r.Sequence
		lastAt = r.RegisteredAt
	}
	for _, i := range h.Intents {
		r, ok := refs[i.Command.RegistrationID]
		if !ok || r.SHA256 != i.Command.RegistrationSHA256 || r.CandidateSHA256 != i.Command.Job.Plan.Candidate.SHA256 || i.CreatedAt.Before(r.RegisteredAt) {
			return ErrInvalid
		}
		used[r.ID] = true
	}
	if len(used) != len(refs) {
		return ErrInvalid
	}
	return nil
}
