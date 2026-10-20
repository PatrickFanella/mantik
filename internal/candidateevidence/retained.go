package candidateevidence

import (
	"reflect"
	"regexp"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/candidateattempt"
	"git.subcult.tv/subculture-collective/mantik/internal/review"
	"git.subcult.tv/subculture-collective/mantik/internal/workerplan"
)

const OutcomeSchema = "mantik-candidate-worker-outcome-v1"
const MaxOutcomeBytes = 8192

var receiverID = regexp.MustCompile(`^receiver-[a-zA-Z0-9_-]{16,64}$`)
var digest = regexp.MustCompile(`^[a-f0-9]{64}$`)

type Report struct {
	ReceiverID   string  `json:"receiver_id"`
	RequestID    string  `json:"request_id"`
	IntentSHA256 string  `json:"intent_sha256"`
	Capture      Capture `json:"capture"`
}

func (r Report) Validate() error {
	if r.Capture.Validate() != nil {
		return ErrConflict
	}
	i := r.Capture.Attempt.Intent.ServiceIntent
	if r.ReceiverID != i.Command.ReceiverID || r.RequestID != i.Command.RequestID || r.IntentSHA256 != i.SHA256 {
		return ErrConflict
	}
	return nil
}

// OutcomeRecord summarizes a retained caller report, never a service execution.
type OutcomeRecord struct {
	Schema                string    `json:"schema"`
	ReceiverID            string    `json:"receiver_id"`
	RequestID             string    `json:"request_id"`
	IntentSequence        int       `json:"intent_sequence"`
	IntentSHA256          string    `json:"intent_sha256"`
	CaptureSHA256         string    `json:"capture_sha256"`
	JobSHA256             string    `json:"job_sha256"`
	JournalID             string    `json:"journal_id"`
	JournalRootSHA256     string    `json:"journal_root_sha256"`
	LocalIntentSHA256     string    `json:"local_intent_sha256"`
	LocalOutcomeSHA256    string    `json:"local_outcome_sha256"`
	AcquisitionSHA256     string    `json:"acquisition_sha256"`
	Status                string    `json:"status"`
	ReasonCode            string    `json:"reason_code"`
	WorkerResultSHA256    string    `json:"worker_result_sha256,omitempty"`
	WorkerStatus          string    `json:"worker_status,omitempty"`
	RecordedAt            time.Time `json:"recorded_at"`
	Authority             string    `json:"authority"`
	ExecutionVerified     bool      `json:"execution_verified"`
	CurrentHostStateKnown bool      `json:"current_host_state_known"`
	QualificationCredit   bool      `json:"qualification_credit"`
	SHA256                string    `json:"sha256"`
}

func (r OutcomeRecord) Validate() error {
	if r.Schema != OutcomeSchema || !receiverID.MatchString(r.ReceiverID) || !review.ID.MatchString(r.RequestID) || r.IntentSequence < 1 || r.IntentSequence > candidateattempt.MaxAttempts || !journalIdentity.MatchString(r.JournalID) || r.RecordedAt.IsZero() || r.RecordedAt.Year() < 1 || r.RecordedAt.Year() > 9999 || r.Authority != "retained_caller_reported_candidate_outcome_only" || r.ExecutionVerified || r.CurrentHostStateKnown || r.QualificationCredit || !review.ID.MatchString(r.ReasonCode) {
		return ErrConflict
	}
	for _, sha := range []string{r.IntentSHA256, r.CaptureSHA256, r.JobSHA256, r.JournalRootSHA256, r.LocalIntentSHA256, r.LocalOutcomeSHA256, r.AcquisitionSHA256} {
		if !digest.MatchString(sha) {
			return ErrConflict
		}
	}
	switch r.Status {
	case "completed", "mismatch", "failed", "incomplete", "cancelled", "timed_out", "wait_interrupted":
	default:
		return ErrConflict
	}
	if r.WorkerResultSHA256 != "" && (!digest.MatchString(r.WorkerResultSHA256) || r.WorkerStatus != "completed" && r.WorkerStatus != "mismatch" && r.WorkerStatus != "failed") || r.WorkerResultSHA256 == "" && r.WorkerStatus != "" {
		return ErrConflict
	}
	sha := r.SHA256
	r.SHA256 = ""
	if sha != workerplan.Hash(r) {
		return ErrConflict
	}
	return nil
}

// RetainOutcome requires the service's own retained intent, not a caller's copy.
// The backend must also validate that intent against its registry/dataset snapshot.
func RetainOutcome(expected candidateattempt.Intent, c Capture, at time.Time) (OutcomeRecord, error) {
	if c.VerifyIntent(expected) != nil || at.IsZero() || at.UTC().Year() < 1 || at.UTC().Year() > 9999 || at.Before(c.Attempt.Outcome.CapturedAt) {
		return OutcomeRecord{}, ErrConflict
	}
	i, o := c.Attempt.Intent, c.Attempt.Outcome
	r := OutcomeRecord{Schema: OutcomeSchema, ReceiverID: expected.Command.ReceiverID, RequestID: expected.Command.RequestID, IntentSequence: expected.Sequence, IntentSHA256: expected.SHA256, CaptureSHA256: c.SHA256, JobSHA256: expected.Command.Job.SHA256, JournalID: i.JournalID, JournalRootSHA256: workerplan.Digest([]byte(c.JournalRoot)), LocalIntentSHA256: i.SHA256, LocalOutcomeSHA256: o.SHA256, AcquisitionSHA256: c.Acquisition.SHA256, Status: o.Receipt.Status, ReasonCode: o.Receipt.ReasonCode, WorkerResultSHA256: o.Receipt.WorkerResultSHA256, WorkerStatus: o.Receipt.WorkerStatus, RecordedAt: at.UTC(), Authority: "retained_caller_reported_candidate_outcome_only"}
	r.SHA256 = workerplan.Hash(r)
	return r, r.Validate()
}

func (r OutcomeRecord) VerifyCapture(expected candidateattempt.Intent, c Capture) error {
	if r.Validate() != nil {
		return ErrConflict
	}
	actual, err := RetainOutcome(expected, c, r.RecordedAt)
	if err != nil || !reflect.DeepEqual(actual, r) {
		return ErrConflict
	}
	return nil
}
