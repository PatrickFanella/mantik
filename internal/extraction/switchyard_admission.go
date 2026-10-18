package extraction

import (
	"bytes"
	"encoding/json"
	"git.subcult.tv/subculture-collective/mantik/internal/news"
	"git.subcult.tv/subculture-collective/mantik/internal/newscase"
	"reflect"
	"strings"
)

const SwitchyardAdmissionSchema = "mantik-reviewed-switchyard-capture-admission-v1"
const SwitchyardApprovalCode = "approve_switchyard_claim_candidates_v1"
const SwitchyardAdmissionAuthority = "reviewed_caller_reported_switchyard_claims_only"

type SwitchyardAdmissionCommand struct {
	ReceiverID    string `json:"receiver_id"`
	CaptureID     string `json:"capture_id"`
	CaptureSHA256 string `json:"capture_sha256"`
	ResultSHA256  string `json:"result_sha256"`
	ApprovalCode  string `json:"approval_code"`
}
type SwitchyardAdmission struct {
	Schema              string                              `json:"schema"`
	Command             SwitchyardAdmissionCommand          `json:"command"`
	Provenance          news.SwitchyardExtractionProvenance `json:"provenance"`
	NewsReceipt         newscase.Receipt                    `json:"news_receipt"`
	Authority           string                              `json:"authority"`
	ExecutionAttested   bool                                `json:"execution_attested"`
	QualificationCredit bool                                `json:"qualification_credit"`
	SHA256              string                              `json:"sha256"`
}

func PrepareSwitchyardAdmission(c SwitchyardAdmissionCommand, a SwitchyardCapture) (news.Recording, error) {
	if a.Validate() != nil || !receiverID.MatchString(c.ReceiverID) || !ID.MatchString(c.CaptureID) || !sha.MatchString(c.CaptureSHA256) || !sha.MatchString(c.ResultSHA256) || c.ApprovalCode != SwitchyardApprovalCode {
		return news.Recording{}, ErrInvalid
	}
	if c.ReceiverID != a.ReceiverID || c.CaptureID != a.ID || c.CaptureSHA256 != a.SHA256 || a.Finished == nil || a.Inspection.State != "finished" || a.Finished.Disposition != "response_complete" || a.Finished.Result == nil || a.Finished.Result.Outcome != "extracted" || c.ResultSHA256 != a.Finished.Result.SHA256 {
		return news.Recording{}, ErrConflict
	}
	f := a.Finished
	if f.Anchoring == nil || f.Anchoring.Status != "derived" || f.ModelArtifact == nil {
		return news.Recording{}, ErrInvalid
	}
	raw, err := inspectBytes(a.Prepared.Request, maxLocalRequestBytes)
	if err != nil {
		return news.Recording{}, err
	}
	var request localRequest
	if json.Unmarshal(raw, &request) != nil || len(request.Messages) != 2 {
		return news.Recording{}, ErrInvalid
	}
	source, err := news.ReadArticle(bytes.NewReader([]byte(request.Messages[1].Content)))
	if err != nil {
		return news.Recording{}, ErrInvalid
	}
	provenance := news.SwitchyardExtractionProvenance{Schema: news.SwitchyardExtractionProvenanceSchema, ReceiverID: a.ReceiverID, CaptureID: a.ID, CaptureSHA256: a.SHA256, PreparedSHA256: a.Prepared.SHA256, FinishedSHA256: f.SHA256, ResultSHA256: f.Result.SHA256, ArticleSHA256: a.Prepared.ArticleSHA256, ModelArtifactSHA256: f.ModelArtifact.SHA256, CandidateArtifactSHA256: f.Anchoring.CandidateArtifactSHA256, ContractSHA256: a.Prepared.ContractSHA256, PromptVersion: a.Prepared.PromptVersion, Authority: SwitchyardCaptureAuthority}
	s := source
	r := news.Recording{Schema: news.RecordingSchema, SourceID: s.SourceID, ArticleID: s.ArticleID, Revision: s.Revision, Kind: s.Kind, Supersedes: s.Supersedes, Publisher: s.Publisher, URL: s.URL, PublishedAt: s.PublishedAt, AvailableAt: s.AvailableAt, ReceivedAt: s.ReceivedAt, EvidenceClass: s.EvidenceClass, AccessRef: s.AccessRef, RawRef: s.RawRef, Original: s.Original, OriginalSHA256: s.OriginalSHA256, ExtractionMethod: news.ReviewedSwitchyardExtractionMethod, Claims: copyJSON(f.Result.Claims), SwitchyardExtraction: &provenance}
	if _, err = news.Normalize(r); err != nil {
		return news.Recording{}, ErrInvalid
	}
	return copyJSON(r), nil
}
func MakeSwitchyardAdmission(c SwitchyardAdmissionCommand, a SwitchyardCapture, record newscase.Record) (SwitchyardAdmission, error) {
	r, err := PrepareSwitchyardAdmission(c, a)
	if err != nil {
		return SwitchyardAdmission{}, err
	}
	normalized, err := news.Normalize(r)
	// Import time is server evidence. Caller-reported completion time is retained
	// but is not used as a service execution/deadline or approval-clock authority.
	if err != nil || !reflect.DeepEqual(record.Recording, r) || !reflect.DeepEqual(record.Result, normalized) || record.Receipt.RecordKey != newscase.RecordKey(r) || record.Receipt.RecordingSHA256 != normalized.RecordingSHA256 || record.Receipt.Duplicate || !validAt(record.Receipt.AcceptedAt) || record.Receipt.AcceptedAt.Before(a.ImportedAt) || len(record.Receipt.Matches) < 1 || len(record.Receipt.Matches) > maxAdmissionMatches {
		return SwitchyardAdmission{}, ErrInvalid
	}
	seen := map[string]bool{}
	for _, m := range record.Receipt.Matches {
		if !strings.HasPrefix(m.CaseID, "news-case-") || !sha.MatchString(strings.TrimPrefix(m.CaseID, "news-case-")) || seen[m.CaseID] || m.Version < 1 || (m.Transition != "opened" && m.Transition != "updated" && m.Transition != "reopened") {
			return SwitchyardAdmission{}, ErrInvalid
		}
		seen[m.CaseID] = true
	}
	if !seen[newscase.CaseID(r.EvidenceClass, a.Prepared.EventKey)] {
		return SwitchyardAdmission{}, ErrInvalid
	}
	approval := SwitchyardAdmission{Schema: SwitchyardAdmissionSchema, Command: c, Provenance: *r.SwitchyardExtraction, NewsReceipt: copyJSON(record.Receipt), Authority: SwitchyardAdmissionAuthority}
	approval.SHA256 = hash(approval)
	return approval, nil
}
func (r SwitchyardAdmission) Validate(a SwitchyardCapture, record newscase.Record) error {
	expected, err := MakeSwitchyardAdmission(r.Command, a, record)
	if err != nil || !reflect.DeepEqual(r, expected) {
		return ErrInvalid
	}
	return nil
}
func (r SwitchyardAdmission) ValidateMetadata() error {
	c := r.Command
	p := r.Provenance
	if r.Schema != SwitchyardAdmissionSchema || c.ApprovalCode != SwitchyardApprovalCode || !receiverID.MatchString(c.ReceiverID) || !ID.MatchString(c.CaptureID) || r.Authority != SwitchyardAdmissionAuthority || r.ExecutionAttested || r.QualificationCredit || r.NewsReceipt.Duplicate || !validAt(r.NewsReceipt.AcceptedAt) || len(r.NewsReceipt.Matches) < 1 || len(r.NewsReceipt.Matches) > maxAdmissionMatches || p.Schema != news.SwitchyardExtractionProvenanceSchema || p.ReceiverID != c.ReceiverID || p.CaptureID != c.CaptureID || p.CaptureSHA256 != c.CaptureSHA256 || p.ResultSHA256 != c.ResultSHA256 || p.Authority != SwitchyardCaptureAuthority || p.ExecutionAttested || p.QualificationCredit || p.PromptVersion != LocalPromptVersion {
		return ErrInvalid
	}
	for _, s := range []string{c.CaptureSHA256, c.ResultSHA256, p.PreparedSHA256, p.FinishedSHA256, p.ArticleSHA256, p.ModelArtifactSHA256, p.CandidateArtifactSHA256, p.ContractSHA256, r.NewsReceipt.RecordKey, r.NewsReceipt.RecordingSHA256, r.SHA256} {
		if !sha.MatchString(s) {
			return ErrInvalid
		}
	}
	digest := r.SHA256
	r.SHA256 = ""
	if digest != hash(r) {
		return ErrInvalid
	}
	return nil
}
