package socialwatch

import (
	"git.subcult.tv/subculture-collective/mantik/internal/paperaccount"
	"git.subcult.tv/subculture-collective/mantik/internal/workerplan"
	"time"
)

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
const copyResearchSourceMethod = "owned-current-copy-research-source-v1"

// Legacy receipts retain their original semantics. New issuance binds both the
// selected capture and any canonical deduplicated request to current, causal
// source versions at issuance; a new observation cannot revive an invalid source.

func validateResearchRequestSource(j Journal, e Entry) error {
	if e.ResearchRequestMethod == "" {
		if e.Research != nil {
			return ErrInvalid
		}
		return nil
	}
	if e.ResearchRequestMethod != researchRequestSourceMethod && e.ResearchRequestMethod != copyResearchSourceMethod {
		return ErrInvalid
	}
	if e.ResearchRequestMethod == copyResearchSourceMethod && e.Research == nil {
		return ErrInvalid
	}
	var sourceID, canonicalID string
	if e.Research != nil {
		sourceID = e.Research.Command.CaptureID
		canonicalID = e.Research.Request.CaptureID
	} else if e.Capture != nil && e.Capture.Command.RequestResearch && e.Capture.ResearchRequest != nil {
		sourceID = e.Capture.Command.ID
		canonicalID = e.Capture.ResearchRequest.CaptureID
	} else {
		return ErrInvalid
	}
	for _, id := range []string{sourceID, canonicalID} {
		view, err := inspectApplicability(j, id, e.At, e.SHA256)
		expected := "current_source_assertion_dispatch_unbound"
		if e.ResearchRequestMethod == copyResearchSourceMethod {
			expected = "copy_source_mapping_and_admission_unavailable"
		}
		if err != nil || view.Status != expected {
			return ErrConflict
		}
	}
	return nil
}

// Callers select an immutable retained capture. Source bytes, watch policy,
// evidence class, deduplication and all request facts are derived by the owner.
type ResearchCommand struct {
	ID            string `json:"id"`
	ReceiverID    string `json:"receiver_id"`
	CaptureID     string `json:"capture_id"`
	CaptureSHA256 string `json:"capture_sha256"`
}

func (c ResearchCommand) Validate() error {
	if !paperaccount.ID.MatchString(c.ID) || !receiverPattern.MatchString(c.ReceiverID) || !paperaccount.ID.MatchString(c.CaptureID) || !pin.MatchString(c.CaptureSHA256) {
		return ErrInvalid
	}
	return nil
}

type ResearchSelection struct {
	Schema                   string          `json:"schema"`
	Command                  ResearchCommand `json:"command"`
	RequestedAt              time.Time       `json:"requested_at"`
	Request                  ResearchRequest `json:"request"`
	ResearchRequestDuplicate bool            `json:"research_request_duplicate"`
	SHA256                   string          `json:"sha256"`
}

func (p *projection) researchForSource(r CaptureRecord, at time.Time) (ResearchRequest, bool, error) {
	c, o := r.Command, r.Source
	watch, ok := p.policies[c.WatchID]
	if !ok || watch.SHA256 != c.PolicySHA256 || !r.SourceAccepted || r.PolicyStatus != "dispatch_unbound" || watch.Command.Policy.Behavior != "research" || o.Activity != nil {
		return ResearchRequest{}, false, ErrConflict
	}
	key := workerplan.Hash(struct{ ReceiverID, EvidenceClass, ClaimGroupSHA256 string }{c.ReceiverID, c.EvidenceClass, r.ClaimGroupSHA256})
	request, duplicate := p.research[key]
	if !duplicate {
		request = ResearchRequest{Schema: "mantik-owned-social-research-request-v1", ID: "social-research-" + key, ReceiverID: c.ReceiverID, WatchID: c.WatchID, PolicySHA256: c.PolicySHA256, CaptureID: c.ID, OriginalSHA256: c.OriginalSHA256, SourceSemanticSHA256: r.SourceSemanticSHA256, ClaimGroupSHA256: r.ClaimGroupSHA256, Source: o.Source, Root: r.Root, RequestedAt: at, EvidenceClass: c.EvidenceClass, Status: "pending_research", Authority: "service_owned_recorded_source_research_request_only"}
		request.SHA256 = workerplan.Hash(request)
		p.research[key] = request
	}
	return clone(request), duplicate, nil
}
func (p *projection) selectResearch(c ResearchCommand, at time.Time, method string) (ResearchSelection, error) {
	source, ok := p.captures[c.CaptureID]
	if !ok {
		return ResearchSelection{}, ErrNotFound
	}
	if source.SHA256 != c.CaptureSHA256 || source.Command.ReceiverID != c.ReceiverID {
		return ResearchSelection{}, ErrConflict
	}
	var request ResearchRequest
	var duplicate bool
	var err error
	schema := "mantik-owned-social-research-selection-v1"
	if method == copyResearchSourceMethod {
		request, duplicate, err = p.researchForCopy(source, at)
		schema = "mantik-owned-copy-research-selection-v1"
	} else {
		request, duplicate, err = p.researchForSource(source, at)
	}
	if err != nil {
		return ResearchSelection{}, err
	}
	result := ResearchSelection{Schema: schema, Command: c, RequestedAt: at, Request: request, ResearchRequestDuplicate: duplicate}
	result.SHA256 = workerplan.Hash(result)
	return result, nil
}

// Copy manifests remain assertions about an external actor. A pending research
// request may inspect them without accepting their instrument or quantity claims.
// Capture-time opt-in keeps its original social-only contract; copy selection is
// a separate versioned retained-source operation, replayed from owner identities.
func (p *projection) researchForCopy(r CaptureRecord, at time.Time) (ResearchRequest, bool, error) {
	c, o := r.Command, r.Source
	watch, ok := p.policies[c.WatchID]
	if !ok || watch.SHA256 != c.PolicySHA256 || watch.Command.Policy.Behavior != "research" || !r.SourceAccepted || r.PolicyStatus != "copy_source_mapping_and_admission_unavailable" || o.Schema != CopyRecordingSchema || o.Activity == nil || r.Copy == nil || r.Copy.FollowStatus != "research_only" {
		return ResearchRequest{}, false, ErrConflict
	}
	key := workerplan.Hash(struct{ Method, ReceiverID, EvidenceClass, ClaimGroupSHA256 string }{copyResearchSourceMethod, c.ReceiverID, c.EvidenceClass, r.ClaimGroupSHA256})
	request, duplicate := p.research[key]
	if !duplicate {
		request = ResearchRequest{Schema: "mantik-owned-copy-research-request-v1", ID: "copy-research-" + key, ReceiverID: c.ReceiverID, WatchID: c.WatchID, PolicySHA256: c.PolicySHA256, CaptureID: c.ID, OriginalSHA256: c.OriginalSHA256, SourceSemanticSHA256: r.SourceSemanticSHA256, ClaimGroupSHA256: r.ClaimGroupSHA256, Source: o.Source, Root: r.Root, RequestedAt: at, EvidenceClass: c.EvidenceClass, Status: "pending_research", Authority: "service_owned_recorded_copy_research_request_only"}
		request.SHA256 = workerplan.Hash(request)
		p.research[key] = request
	}
	return clone(request), duplicate, nil
}
