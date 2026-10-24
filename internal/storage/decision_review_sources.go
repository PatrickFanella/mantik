package storage

import (
	"encoding/json"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/socialwatch"
	"git.subcult.tv/subculture-collective/mantik/internal/workerplan"
)

// This binds a caller's typed assertions to actual retained source provenance.
// It does not assert that the source text contains those assertions.
type DecisionReviewSourceBinding struct {
	Kind                     string `json:"kind"`
	CaptureID                string `json:"capture_id"`
	CaptureSHA256            string `json:"capture_sha256"`
	OriginalSHA256           string `json:"original_sha256"`
	PolicySHA256             string `json:"policy_sha256"`
	ClaimGroupSHA256         string `json:"claim_group_sha256"`
	JournalEntries           int    `json:"journal_entries"`
	JournalHeadSHA256        string `json:"journal_head_sha256"`
	ApplicabilitySHA256      string `json:"applicability_sha256"`
	RecordPath               string `json:"record_path"`
	ClaimsExtractionVerified bool   `json:"claims_extraction_verified"`
	ProviderAccessVerified   bool   `json:"provider_access_verified"`
}

func decisionReviewNeedsSources(c DecisionReviewCommand, all map[string]DecisionReviewRecord) bool {
	if c.Source != nil {
		return true
	}
	for _, r := range all {
		if r.Source != nil || r.Command.Source != nil {
			return true
		}
	}
	return false
}
func (m *Memory) decisionReviewSocialSourcesLocked(c DecisionReviewCommand, all map[string]DecisionReviewRecord) ([]socialwatch.Journal, error) {
	if !decisionReviewNeedsSources(c, all) {
		return nil, nil
	}
	j, err := m.socialJournalLocked()
	if err != nil {
		return nil, err
	}
	return []socialwatch.Journal{j}, nil
}
func sourceDecisionReview(c DecisionReviewCommand, d PaperDecisionRecord, at time.Time, sources []socialwatch.Journal) (DecisionReviewRecord, error) {
	if c.Validate() != nil {
		return DecisionReviewRecord{}, ErrDecisionReviewInvalid
	}
	if c.Source == nil {
		return newDecisionReview(c, d, at)
	}
	if len(sources) != 1 || sources[0].ReceiverID != c.ReceiverID {
		return DecisionReviewRecord{}, ErrDecisionReviewConflict
	}
	journal := clone(sources[0])
	if _, err := socialwatch.Read(journal); err != nil {
		return DecisionReviewRecord{}, err
	}
	// The immutable owner clock fence prevents later social writes from entering
	// an already retained review cut. Equal source timestamps retain journal order.
	end := 0
	for _, e := range journal.Entries {
		if e.At.After(at) {
			break
		}
		end++
	}
	journal.Entries = journal.Entries[:end]
	catalog, err := socialwatch.Read(journal)
	if err != nil {
		return DecisionReviewRecord{}, err
	}
	var capture *socialwatch.CaptureRecord
	for _, entry := range catalog.History {
		if entry.Capture != nil && entry.Capture.Command.ID == c.Source.CaptureID {
			capture = entry.Capture
			break
		}
	}
	if capture == nil || capture.SHA256 != c.Source.CaptureSHA256 {
		return DecisionReviewRecord{}, ErrDecisionReviewConflict
	}
	applicability, err := socialwatch.InspectApplicability(journal, c.Source.CaptureID, at)
	eligible := applicability.Status == "current_source_assertion_dispatch_unbound" || capture.Copy != nil && applicability.Status == "copy_source_mapping_and_admission_unavailable"
	if err != nil || !eligible {
		return DecisionReviewRecord{}, ErrDecisionReviewConflict
	}
	authorized := false
	for _, watch := range catalog.Watches {
		if watch.Policy.Command.Policy.ID == capture.Command.WatchID && watch.Policy.SHA256 == capture.Command.PolicySHA256 && watch.Policy.Command.Policy.Enabled && watch.Policy.Command.Policy.Behavior == "decision_review" {
			authorized = true
		}
	}
	if !authorized {
		return DecisionReviewRecord{}, ErrDecisionReviewConflict
	}
	base := clone(c)
	base.Source = nil
	r, err := newDecisionReview(base, d, at)
	if err != nil {
		return DecisionReviewRecord{}, err
	}
	kind, path := "social", "/v1/social-recordings/"
	if capture.Copy != nil {
		kind, path = "copy", "/v1/copy-recordings/"
	}
	r.Schema = "mantik-finalized-decision-review-v2"
	r.Method = "owned-social-copy-decision-claims-v1"
	r.Command = clone(c)
	r.Source = &DecisionReviewSourceBinding{Kind: kind, CaptureID: capture.Command.ID, CaptureSHA256: capture.SHA256, OriginalSHA256: capture.Command.OriginalSHA256, PolicySHA256: capture.Command.PolicySHA256, ClaimGroupSHA256: capture.ClaimGroupSHA256, JournalEntries: len(journal.Entries), JournalHeadSHA256: catalog.HeadSHA256, ApplicabilitySHA256: applicability.SHA256, RecordPath: path + capture.Command.ID}
	r.SHA256 = ""
	r.SHA256 = workerplan.Hash(r)
	// Include the binding in the same existing record byte bound.
	if raw, e := json.Marshal(r); e != nil || len(raw) > MaxDecisionReviewBytes {
		return DecisionReviewRecord{}, ErrDecisionReviewCapacity
	}
	return r, nil
}

// Source writers use only validated retained envelopes as a clock floor; this
// is not a replay, admission check or financial authority. Full review replay
// independently derives each source binding from the actual owner history.
func socialDecisionReviewClock(owner string, all map[string]DecisionReviewRecord, at time.Time) error {
	if len(all) > MaxDecisionReviews {
		return ErrDecisionReviewInvalid
	}
	for id, r := range all {
		unsigned := clone(r)
		unsigned.SHA256 = ""
		if id != r.Command.ID || r.Command.Validate() != nil || r.Command.ReceiverID != owner || !declarationTime(r.ReviewedAt) || workerplan.Hash(unsigned) != r.SHA256 || r.ProseVerified || r.ModelUsed || r.AdmissionAvailable || r.ExecutionAvailable || r.QualificationCredit {
			return ErrDecisionReviewInvalid
		}
		if r.Source == nil {
			if r.Schema != "mantik-finalized-decision-review-v1" || r.Command.Source != nil {
				return ErrDecisionReviewInvalid
			}
			continue
		}
		if r.Schema != "mantik-finalized-decision-review-v2" || r.Command.Source == nil || r.Source.ClaimsExtractionVerified || r.Source.ProviderAccessVerified {
			return ErrDecisionReviewInvalid
		}
		if !at.After(r.ReviewedAt) {
			return socialwatch.ErrConflict
		}
	}
	return nil
}
