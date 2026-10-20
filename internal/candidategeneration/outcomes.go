package candidategeneration

import (
	"encoding/json"
	"io"
	"reflect"
	"slices"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/review"
	"git.subcult.tv/subculture-collective/mantik/internal/workerplan"
)

const ReportSchema = "mantik-candidate-generation-report-v1"
const OutcomeSchema = "mantik-candidate-generation-outcome-v1"
const OutcomeArtifactSchema = "mantik-candidate-generation-outcome-artifact-v1"
const OutcomeHistorySchema = "mantik-candidate-generation-outcome-history-v1"
const MaxReportBytes = MaxRecordingBytes + 1024
const MaxOutcomeBytes = 8192
const MaxOutcomeArtifactBytes = MaxIntentBytes + MaxRecordingBytes + MaxOutcomeBytes + 1024
const MaxOutcomeHistoryBytes = MaxIntentHistoryBytes + MaxIntents*(MaxOutcomeBytes+256) + 4096

// Report is caller-reported evidence, not proof that a provider was called.
type Report struct {
	Schema       string    `json:"schema"`
	ReceiverID   string    `json:"receiver_id"`
	RequestID    string    `json:"request_id"`
	IntentSHA256 string    `json:"intent_sha256"`
	Recording    Recording `json:"recording"`
}

func (r Report) Validate() error {
	if r.Schema != ReportSchema || !receiver.MatchString(r.ReceiverID) || !review.ID.MatchString(r.RequestID) || !digest.MatchString(r.IntentSHA256) || r.Recording.Validate() != nil {
		return ErrInvalid
	}
	return nil
}
func ReadReport(input io.Reader) (Report, error) {
	raw, err := io.ReadAll(io.LimitReader(input, MaxReportBytes+1))
	var r Report
	if err != nil || workerplan.ReadBoundedShape(raw, &r, MaxReportBytes) != nil || r.Validate() != nil {
		return Report{}, ErrInvalid
	}
	return r, nil
}

type Outcome struct {
	Schema                  string    `json:"schema"`
	ReceiverID              string    `json:"receiver_id"`
	RequestID               string    `json:"request_id"`
	IntentSequence          int       `json:"intent_sequence"`
	IntentSHA256            string    `json:"intent_sha256"`
	PlanSHA256              string    `json:"plan_sha256"`
	RecordingSHA256         string    `json:"recording_sha256"`
	OutputSHA256            string    `json:"output_sha256"`
	OutputBytes             int       `json:"output_bytes"`
	CandidateSHA256         string    `json:"candidate_sha256,omitempty"`
	CapturedAt              time.Time `json:"captured_at"`
	RecordedAt              time.Time `json:"recorded_at"`
	Status                  string    `json:"status"`
	ReasonCode              string    `json:"reason_code"`
	Authority               string    `json:"authority"`
	ProviderCalled          bool      `json:"provider_called"`
	ModelIdentityVerified   bool      `json:"model_identity_verified"`
	ModelAuthorshipVerified bool      `json:"model_authorship_verified"`
	SourceCompiled          bool      `json:"source_compiled"`
	SourceExecuted          bool      `json:"source_executed"`
	QualificationCredit     bool      `json:"qualification_credit"`
	SHA256                  string    `json:"sha256"`
}

func (o Outcome) Validate() error {
	if o.Schema != OutcomeSchema || !receiver.MatchString(o.ReceiverID) || !review.ID.MatchString(o.RequestID) || o.IntentSequence < 1 || o.IntentSequence > MaxIntents || !digest.MatchString(o.IntentSHA256) || !digest.MatchString(o.PlanSHA256) || !digest.MatchString(o.RecordingSHA256) || !digest.MatchString(o.OutputSHA256) || o.OutputBytes < 0 || o.OutputBytes > MaxOutputBytes || !validTime(o.CapturedAt) || !validTime(o.RecordedAt) || o.RecordedAt.Before(o.CapturedAt) || o.Authority != "retained_caller_reported_candidate_generation_only" || o.ProviderCalled || o.ModelIdentityVerified || o.ModelAuthorshipVerified || o.SourceCompiled || o.SourceExecuted || o.QualificationCredit {
		return ErrInvalid
	}
	switch o.Status {
	case "candidate_recorded":
		if o.ReasonCode != "source_payload_retained" || !digest.MatchString(o.CandidateSHA256) || o.OutputBytes == 0 {
			return ErrInvalid
		}
	case "output_rejected":
		if o.CandidateSHA256 != "" {
			return ErrInvalid
		}
		switch o.ReasonCode {
		case "missing_output":
			if o.OutputBytes != 0 || o.OutputSHA256 != workerplan.Digest(nil) {
				return ErrInvalid
			}
		case "invalid_output", "invalid_source":
			if o.OutputBytes == 0 {
				return ErrInvalid
			}
		default:
			return ErrInvalid
		}
	default:
		return ErrInvalid
	}
	sha := o.SHA256
	o.SHA256 = ""
	raw, err := json.Marshal(o)
	if err != nil || len(raw) > MaxOutcomeBytes || sha != workerplan.Hash(o) {
		return ErrInvalid
	}
	return nil
}
func (o Outcome) VerifyIntent(i Intent) error {
	if o.Validate() != nil || i.Validate() != nil || o.ReceiverID != i.Command.ReceiverID || o.RequestID != i.Command.RequestID || o.IntentSequence != i.Sequence || o.IntentSHA256 != i.SHA256 || o.PlanSHA256 != i.Command.Plan.SHA256 || o.CapturedAt.Before(i.CreatedAt) {
		return ErrConflict
	}
	return nil
}

// NewOutcome compares the full caller recording with the owner's retained intent.
// The owner must validate its authoritative publication and exact dataset before
// invoking this constructor; portable hashes alone do not prove those facts.
func NewOutcome(i Intent, r Report, at time.Time) (Outcome, error) {
	if r.Validate() != nil || i.Validate() != nil {
		return Outcome{}, ErrInvalid
	}
	rec := r.Recording
	if r.ReceiverID != i.Command.ReceiverID || r.RequestID != i.Command.RequestID || r.IntentSHA256 != i.SHA256 || !reflect.DeepEqual(rec.Plan, i.Command.Plan) || rec.CapturedAt.Before(i.CreatedAt) || !validTime(at) || at.Before(rec.CapturedAt) {
		return Outcome{}, ErrConflict
	}
	o := Outcome{Schema: OutcomeSchema, ReceiverID: r.ReceiverID, RequestID: r.RequestID, IntentSequence: i.Sequence, IntentSHA256: i.SHA256, PlanSHA256: rec.Plan.SHA256, RecordingSHA256: rec.SHA256, OutputSHA256: rec.OutputSHA256, OutputBytes: rec.OutputBytes, CapturedAt: rec.CapturedAt, RecordedAt: at.UTC(), Status: rec.Status, ReasonCode: rec.ReasonCode, Authority: "retained_caller_reported_candidate_generation_only"}
	if rec.Candidate != nil {
		o.CandidateSHA256 = rec.Candidate.SHA256
	}
	o.SHA256 = workerplan.Hash(o)
	return o, o.Validate()
}

type OutcomeArtifact struct {
	Schema    string    `json:"schema"`
	Intent    Intent    `json:"intent"`
	Outcome   Outcome   `json:"outcome"`
	Recording Recording `json:"recording"`
}

func NewOutcomeArtifact(i Intent, o Outcome, r Recording) (OutcomeArtifact, error) {
	a := OutcomeArtifact{Schema: OutcomeArtifactSchema, Intent: i, Outcome: o, Recording: r}
	if a.Validate() != nil {
		return OutcomeArtifact{}, ErrInvalid
	}
	raw, _ := json.Marshal(a)
	var detached OutcomeArtifact
	_ = json.Unmarshal(raw, &detached)
	return detached, nil
}
func (a OutcomeArtifact) Validate() error {
	if a.Schema != OutcomeArtifactSchema || a.Outcome.VerifyIntent(a.Intent) != nil {
		return ErrInvalid
	}
	r := Report{Schema: ReportSchema, ReceiverID: a.Outcome.ReceiverID, RequestID: a.Outcome.RequestID, IntentSHA256: a.Outcome.IntentSHA256, Recording: a.Recording}
	expected, err := NewOutcome(a.Intent, r, a.Outcome.RecordedAt)
	if err != nil || !reflect.DeepEqual(expected, a.Outcome) {
		return ErrInvalid
	}
	return nil
}
func ReadOutcomeArtifact(input io.Reader) (OutcomeArtifact, error) {
	raw, err := io.ReadAll(io.LimitReader(input, MaxOutcomeArtifactBytes+1))
	var a OutcomeArtifact
	if err != nil || workerplan.ReadBoundedShape(raw, &a, MaxOutcomeArtifactBytes) != nil || a.Validate() != nil {
		return OutcomeArtifact{}, ErrInvalid
	}
	return a, nil
}

type OutcomeHistory struct {
	Schema                  string        `json:"schema"`
	ReceiverID              string        `json:"receiver_id"`
	IntentHistory           IntentHistory `json:"intent_history"`
	Outcomes                []Outcome     `json:"outcomes"`
	UnreportedRequestIDs    []string      `json:"unreported_request_ids"`
	Authority               string        `json:"authority"`
	ProviderCalled          bool          `json:"provider_called"`
	ModelAuthorshipVerified bool          `json:"model_authorship_verified"`
	QualificationCredit     bool          `json:"qualification_credit"`
	SHA256                  string        `json:"sha256"`
}

// OutcomesFor requires full recording/publication/dataset validation in the same
// owner snapshot. An unreported request says nothing about provider activity.
func OutcomesFor(intents IntentHistory, outcomes []Outcome) (OutcomeHistory, error) {
	h := OutcomeHistory{Schema: OutcomeHistorySchema, ReceiverID: intents.ReceiverID, IntentHistory: intents, Outcomes: append([]Outcome{}, outcomes...), UnreportedRequestIDs: []string{}, Authority: "retained_candidate_generation_reports_only"}
	reported := map[string]bool{}
	for _, o := range outcomes {
		reported[o.RequestID] = true
	}
	for _, i := range intents.Intents {
		if !reported[i.Command.RequestID] {
			h.UnreportedRequestIDs = append(h.UnreportedRequestIDs, i.Command.RequestID)
		}
	}
	raw, _ := json.Marshal(h)
	var detached OutcomeHistory
	_ = json.Unmarshal(raw, &detached)
	detached.SHA256 = workerplan.Hash(detached)
	return detached, detached.Validate()
}
func (h OutcomeHistory) Validate() error {
	if h.Schema != OutcomeHistorySchema || h.IntentHistory.Validate() != nil || h.ReceiverID != h.IntentHistory.ReceiverID || h.Outcomes == nil || len(h.Outcomes) > MaxIntents || h.UnreportedRequestIDs == nil || h.Authority != "retained_candidate_generation_reports_only" || h.ProviderCalled || h.ModelAuthorshipVerified || h.QualificationCredit {
		return ErrInvalid
	}
	last := 0
	reported := map[string]bool{}
	for _, o := range h.Outcomes {
		if o.IntentSequence <= last || o.IntentSequence > len(h.IntentHistory.Intents) || o.VerifyIntent(h.IntentHistory.Intents[o.IntentSequence-1]) != nil {
			return ErrInvalid
		}
		last = o.IntentSequence
		reported[o.RequestID] = true
	}
	unreported := []string{}
	for _, i := range h.IntentHistory.Intents {
		if !reported[i.Command.RequestID] {
			unreported = append(unreported, i.Command.RequestID)
		}
	}
	if !slices.Equal(unreported, h.UnreportedRequestIDs) {
		return ErrInvalid
	}
	sha := h.SHA256
	h.SHA256 = ""
	if sha != workerplan.Hash(h) {
		return ErrInvalid
	}
	return nil
}

// AppendOutcome is the pure retry rule for already validated owner snapshots.
// Storage must check every retained full recording and exact dataset first.
func AppendOutcome(intents IntentHistory, all []Outcome, r Report, now func() time.Time) (Outcome, bool, error) {
	if r.Validate() != nil {
		return Outcome{}, false, ErrInvalid
	}
	if _, err := OutcomesFor(intents, all); err != nil {
		return Outcome{}, false, err
	}
	if r.ReceiverID != intents.ReceiverID {
		return Outcome{}, false, ErrConflict
	}
	var selected Intent
	for _, i := range intents.Intents {
		if i.Command.RequestID == r.RequestID {
			selected = i
			break
		}
	}
	if selected.SHA256 == "" {
		return Outcome{}, false, ErrNotFound
	}
	// Compare the report before consulting a caller's clock.
	if _, err := NewOutcome(selected, r, r.Recording.CapturedAt); err != nil {
		return Outcome{}, false, err
	}
	for _, o := range all {
		if o.RequestID == r.RequestID {
			expected, err := NewOutcome(selected, r, o.RecordedAt)
			if err != nil || !reflect.DeepEqual(expected, o) {
				return Outcome{}, false, ErrConflict
			}
			return expected, true, nil
		}
	}
	o, err := NewOutcome(selected, r, now())
	return o, false, err
}
