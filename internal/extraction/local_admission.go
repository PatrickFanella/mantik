package extraction

import (
	"bytes"
	"encoding/json"
	"git.subcult.tv/subculture-collective/mantik/internal/news"
	"git.subcult.tv/subculture-collective/mantik/internal/newscase"
	"reflect"
	"strings"
)

const LocalAdmissionSchema = "mantik-reviewed-local-capture-admission-v1"
const LocalApprovalCode = "approve_local_claim_candidates_v1"
const LocalAdmissionAuthority = "reviewed_caller_reported_local_claims_only"

type LocalAdmissionCommand struct {
	ReceiverID    string `json:"receiver_id"`
	CaptureID     string `json:"capture_id"`
	CaptureSHA256 string `json:"capture_sha256"`
	ResultSHA256  string `json:"result_sha256"`
	ApprovalCode  string `json:"approval_code"`
}
type LocalAdmission struct {
	Schema              string                         `json:"schema"`
	Command             LocalAdmissionCommand          `json:"command"`
	Provenance          news.LocalExtractionProvenance `json:"provenance"`
	NewsReceipt         newscase.Receipt               `json:"news_receipt"`
	Authority           string                         `json:"authority"`
	ExecutionAttested   bool                           `json:"execution_attested"`
	QualificationCredit bool                           `json:"qualification_credit"`
	SHA256              string                         `json:"sha256"`
}

func PrepareLocalAdmission(c LocalAdmissionCommand, a LocalCapture) (news.Recording, error) {
	if a.Validate() != nil || !receiverID.MatchString(c.ReceiverID) || !ID.MatchString(c.CaptureID) || !sha.MatchString(c.CaptureSHA256) || !sha.MatchString(c.ResultSHA256) || c.ApprovalCode != LocalApprovalCode {
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
	provenance := news.LocalExtractionProvenance{Schema: news.LocalExtractionProvenanceSchema, ReceiverID: a.ReceiverID, CaptureID: a.ID, CaptureSHA256: a.SHA256, PreparedSHA256: a.Prepared.SHA256, FinishedSHA256: f.SHA256, ResultSHA256: f.Result.SHA256, ArticleSHA256: a.Prepared.ArticleSHA256, ModelArtifactSHA256: f.ModelArtifact.SHA256, CandidateArtifactSHA256: f.Anchoring.CandidateArtifactSHA256, ContractSHA256: a.Prepared.ContractSHA256, PromptVersion: a.Prepared.PromptVersion, Authority: LocalCaptureAuthority}
	s := source
	r := news.Recording{Schema: news.RecordingSchema, SourceID: s.SourceID, ArticleID: s.ArticleID, Revision: s.Revision, Kind: s.Kind, Supersedes: s.Supersedes, Publisher: s.Publisher, URL: s.URL, PublishedAt: s.PublishedAt, AvailableAt: s.AvailableAt, ReceivedAt: s.ReceivedAt, EvidenceClass: s.EvidenceClass, AccessRef: s.AccessRef, RawRef: s.RawRef, Original: s.Original, OriginalSHA256: s.OriginalSHA256, ExtractionMethod: news.ReviewedLocalExtractionMethod, Claims: copyJSON(f.Result.Claims), LocalExtraction: &provenance}
	if _, err = news.Normalize(r); err != nil {
		return news.Recording{}, ErrInvalid
	}
	return copyJSON(r), nil
}
func MakeLocalAdmission(c LocalAdmissionCommand, a LocalCapture, record newscase.Record) (LocalAdmission, error) {
	r, err := PrepareLocalAdmission(c, a)
	if err != nil {
		return LocalAdmission{}, err
	}
	normalized, err := news.Normalize(r)
	// Import time is server evidence. Caller-reported completion time is retained
	// but is not used as a service execution/deadline or approval-clock authority.
	if err != nil || !reflect.DeepEqual(record.Recording, r) || !reflect.DeepEqual(record.Result, normalized) || record.Receipt.RecordKey != newscase.RecordKey(r) || record.Receipt.RecordingSHA256 != normalized.RecordingSHA256 || record.Receipt.Duplicate || !validAt(record.Receipt.AcceptedAt) || record.Receipt.AcceptedAt.Before(a.ImportedAt) || len(record.Receipt.Matches) < 1 || len(record.Receipt.Matches) > maxAdmissionMatches {
		return LocalAdmission{}, ErrInvalid
	}
	seen := map[string]bool{}
	for _, m := range record.Receipt.Matches {
		if !strings.HasPrefix(m.CaseID, "news-case-") || !sha.MatchString(strings.TrimPrefix(m.CaseID, "news-case-")) || seen[m.CaseID] || m.Version < 1 || (m.Transition != "opened" && m.Transition != "updated" && m.Transition != "reopened") {
			return LocalAdmission{}, ErrInvalid
		}
		seen[m.CaseID] = true
	}
	if !seen[newscase.CaseID(r.EvidenceClass, a.Prepared.EventKey)] {
		return LocalAdmission{}, ErrInvalid
	}
	approval := LocalAdmission{Schema: LocalAdmissionSchema, Command: c, Provenance: *r.LocalExtraction, NewsReceipt: copyJSON(record.Receipt), Authority: LocalAdmissionAuthority}
	approval.SHA256 = hash(approval)
	return approval, nil
}
func (r LocalAdmission) Validate(a LocalCapture, record newscase.Record) error {
	expected, err := MakeLocalAdmission(r.Command, a, record)
	if err != nil || !reflect.DeepEqual(r, expected) {
		return ErrInvalid
	}
	return nil
}
func (r LocalAdmission) ValidateMetadata() error {
	c := r.Command
	p := r.Provenance
	if r.Schema != LocalAdmissionSchema || c.ApprovalCode != LocalApprovalCode || !receiverID.MatchString(c.ReceiverID) || !ID.MatchString(c.CaptureID) || r.Authority != LocalAdmissionAuthority || r.ExecutionAttested || r.QualificationCredit || r.NewsReceipt.Duplicate || !validAt(r.NewsReceipt.AcceptedAt) || len(r.NewsReceipt.Matches) < 1 || len(r.NewsReceipt.Matches) > maxAdmissionMatches || p.Schema != news.LocalExtractionProvenanceSchema || p.ReceiverID != c.ReceiverID || p.CaptureID != c.CaptureID || p.CaptureSHA256 != c.CaptureSHA256 || p.ResultSHA256 != c.ResultSHA256 || p.Authority != LocalCaptureAuthority || p.ExecutionAttested || p.QualificationCredit || p.PromptVersion != LocalPromptVersion {
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
