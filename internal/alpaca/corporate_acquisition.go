package alpaca

import (
	"encoding/json"
	"fmt"
	"time"
)

// These hashes resolve actual retained journal artifacts. They are neither a
// caller assertion of acquisition nor a provider signature or coverage grant.
type CorporateActionAcquisitionAttempt struct {
	Number         int       `json:"number"`
	StartSHA256    string    `json:"start_sha256"`
	ResponseSHA256 string    `json:"response_sha256"`
	StartedAt      time.Time `json:"started_at"`
	EndedAt        time.Time `json:"ended_at"`
	State          string    `json:"state"`
	HTTPStatus     int       `json:"http_status"`
	Partial        bool      `json:"partial"`
}
type CorporateActionAcquisition struct {
	Schema                   string                              `json:"schema"`
	Method                   string                              `json:"method"`
	JournalID                string                              `json:"journal_id"`
	CollectionID             string                              `json:"collection_id"`
	IntentSHA256             string                              `json:"intent_sha256"`
	CompletionSHA256         string                              `json:"completion_sha256"`
	CaptureRef               string                              `json:"capture_ref"`
	CaptureSHA256            string                              `json:"capture_sha256"`
	RecordingSHA256          string                              `json:"recording_sha256"`
	SourceSHA256             string                              `json:"source_sha256"`
	RequestedAt              time.Time                           `json:"requested_at"`
	ResponseAt               time.Time                           `json:"response_at"`
	CompletedAt              time.Time                           `json:"completed_at"`
	AvailableAt              time.Time                           `json:"available_at"`
	Attempts                 []CorporateActionAcquisitionAttempt `json:"attempts"`
	Authority                string                              `json:"authority"`
	ProviderAttested         bool                                `json:"provider_attested"`
	CoverageComplete         bool                                `json:"coverage_complete"`
	EconomicApplicationReady bool                                `json:"economic_application_ready"`
	AdmissionAvailable       bool                                `json:"admission_available"`
	ExecutionAvailable       bool                                `json:"execution_available"`
	QualificationCredit      bool                                `json:"qualification_credit"`
	SHA256                   string                              `json:"sha256"`
}

type CorporateActionAcquiredSource struct {
	Acquisition CorporateActionAcquisition `json:"acquisition"`
	Recording   Recording                  `json:"recording"`
}

// CorporateActionAcquiredSource resolves the original recording and receipt
// from this owned journal. No caller recording or proof is accepted.
func (j *Journal) CorporateActionAcquiredSource(id string) (CorporateActionAcquiredSource, error) {
	if !capturePattern.MatchString(id) {
		return CorporateActionAcquiredSource{}, ErrJournalConflict
	}
	captures, err := j.captures()
	if err != nil {
		return CorporateActionAcquiredSource{}, err
	}
	for _, c := range captures {
		if c.Recording.CaptureID != id {
			continue
		}
		proof, err := j.corporateActionAcquisition(id, &c)
		if err != nil {
			return CorporateActionAcquiredSource{}, err
		}
		return CorporateActionAcquiredSource{proof, c.Recording}, nil
	}
	// Resolve incomplete and failed collections through their actual owner,
	// rather than interpreting a missing capture as a successful empty page.
	_, err = j.CorporateActionAcquisition(id)
	if err == nil {
		err = ErrJournalConflict
	}
	return CorporateActionAcquiredSource{}, err
}

// CorporateActionAcquisition verifies a completed network collection and its
// actual capture, including every bounded attempt. Reads never resume a request
// or promote a manual capture, an unfinished attempt or failed normalization.
func (j *Journal) CorporateActionAcquisition(id string) (CorporateActionAcquisition, error) {
	return j.corporateActionAcquisition(id, nil)
}

// Snapshot inspection already holds a verified immutable capture. Reuse it
// rather than scan and normalize the entire journal a second time per page.
func (j *Journal) corporateActionAcquisition(id string, retained *capture) (CorporateActionAcquisition, error) {
	fail := func(err error) (CorporateActionAcquisition, error) { return CorporateActionAcquisition{}, err }
	status, err := j.Collection(id)
	if err != nil {
		return fail(err)
	}
	if status.State == "failed" {
		return fail(ErrCollectionFailed)
	}
	if status.State != "retained" {
		return fail(ErrCollectionIncomplete)
	}
	var intent collectionIntent
	intentSHA, err := j.readCollection(collectionName(id, "intent"), &intent)
	if err != nil {
		return fail(err)
	}
	if intent.Spec.Capability != "corporate_actions" {
		return fail(ErrJournalConflict)
	}
	var done collectionCompletion
	doneSHA, err := j.readCollection(collectionName(id, "complete"), &done)
	if err != nil {
		return fail(err)
	}
	out := CorporateActionAcquisition{Schema: "mantik-journal-corporate-acquisition-v1", Method: "retained-intent-attempts-capture-completion-v1", JournalID: j.ID, CollectionID: id, IntentSHA256: intentSHA, CompletionSHA256: doneSHA, CaptureRef: "journal:" + j.ID + ":" + done.Capture, RequestedAt: intent.At.UTC(), CompletedAt: done.At.UTC(), Attempts: []CorporateActionAcquisitionAttempt{}, Authority: "journal_owned_recorded_acquisition_only"}
	clock := intent.At
	for n := 1; n <= status.Attempts; n++ {
		var start, response ReadAttempt
		startSHA, err := j.readCollection(collectionName(id, fmt.Sprintf("start-%d", n)), &start)
		if err != nil {
			return fail(err)
		}
		responseSHA, err := j.readCollection(collectionName(id, fmt.Sprintf("response-%d", n)), &response)
		if err != nil {
			return fail(err)
		}
		// Collection validates shape and response-after-start. This receipt also
		// binds the intent, inter-attempt and terminal completion clock order.
		if start.At.Before(clock) || response.At.Before(start.At) {
			return fail(ErrJournalConflict)
		}
		clock = response.At
		out.Attempts = append(out.Attempts, CorporateActionAcquisitionAttempt{n, startSHA, responseSHA, start.At.UTC(), response.At.UTC(), response.State, response.HTTPStatus, response.Partial})
	}
	if done.At.Before(clock) {
		return fail(ErrJournalConflict)
	}
	out.ResponseAt = clock.UTC()
	out.AvailableAt = done.At.UTC()
	captures := []capture{}
	if retained != nil {
		captures = append(captures, *retained)
	} else {
		captures, err = j.captures()
		if err != nil {
			return fail(err)
		}
	}
	for _, c := range captures {
		if captureName(c.Recording) != done.Capture {
			continue
		}
		raw, err := json.Marshal(c.Recording)
		if err != nil {
			return fail(err)
		}
		out.CaptureSHA256 = c.SHA256
		out.RecordingSHA256 = Digest(string(raw))
		out.SourceSHA256 = c.Recording.SHA256
	}
	if out.CaptureSHA256 == "" {
		return fail(ErrJournalConflict)
	}
	raw, err := json.Marshal(out)
	if err != nil {
		return fail(err)
	}
	out.SHA256 = Digest(string(raw))
	return out, nil
}
