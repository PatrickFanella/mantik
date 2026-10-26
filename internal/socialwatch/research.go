package socialwatch

import "time"

// A pending request selects owner-retained source material for research only.
// It is not dispatched work, verified extraction, an independent observation,
// a decision, admission or an instruction to change capital. Research may
// inspect later edits/deletions through the retained source history.
type ResearchRequest struct {
	Schema                    string    `json:"schema"`
	ID                        string    `json:"id"`
	ReceiverID                string    `json:"receiver_id"`
	WatchID                   string    `json:"watch_id"`
	PolicySHA256              string    `json:"policy_sha256"`
	CaptureID                 string    `json:"capture_id"`
	OriginalSHA256            string    `json:"original_sha256"`
	SourceSemanticSHA256      string    `json:"source_semantic_sha256"`
	ClaimGroupSHA256          string    `json:"claim_group_sha256"`
	Source                    SourceRef `json:"source"`
	Root                      SourceRef `json:"root"`
	RequestedAt               time.Time `json:"requested_at"`
	EvidenceClass             string    `json:"evidence_class"`
	Status                    string    `json:"status"`
	Authority                 string    `json:"authority"`
	DispatchAvailable         bool      `json:"dispatch_available"`
	IndependentEvidenceCredit bool      `json:"independent_evidence_credit"`
	AdmissionAvailable        bool      `json:"admission_available"`
	ExecutionAvailable        bool      `json:"execution_available"`
	QualificationCredit       bool      `json:"qualification_credit"`
	SHA256                    string    `json:"sha256"`
}

const researchRequestSourceMethod = "owned-current-social-research-source-v1"

// Legacy receipts retain their original semantics. New issuance binds both the
// selected capture and any canonical deduplicated request to current, causal
// source versions at issuance; a new observation cannot revive an invalid source.
func validateResearchRequestSource(j Journal, e Entry) error {
	if e.ResearchRequestMethod == "" {
		return nil
	}
	if e.ResearchRequestMethod != researchRequestSourceMethod || e.Capture == nil || !e.Capture.Command.RequestResearch || e.Capture.ResearchRequest == nil {
		return ErrInvalid
	}
	for _, id := range []string{e.Capture.Command.ID, e.Capture.ResearchRequest.CaptureID} {
		view, err := inspectApplicability(j, id, e.At, e.SHA256)
		if err != nil || view.Status != "current_source_assertion_dispatch_unbound" {
			return ErrConflict
		}
	}
	return nil
}
