package alpacarecord

import "time"

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
