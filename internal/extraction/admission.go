package extraction

import (
	"reflect"
	"strings"

	"git.subcult.tv/subculture-collective/mantik/internal/news"
	"git.subcult.tv/subculture-collective/mantik/internal/newscase"
)

const AdmissionSchema = "mantik-reviewed-extraction-admission-v1"
const ApprovalCode = "approve_claim_candidates_v1"
const maxAdmissionMatches = 17 // One approved event plus up to 16 predecessor events.

// AdmissionCommand is an explicit approval of the complete immutable result.
// It cannot substitute article metadata, claims, event keys or check records.
// The attempt ID is also the stable identity of its one admission command.
type AdmissionCommand struct {
	ReceiverID    string `json:"receiver_id"`
	AttemptID     string `json:"attempt_id"`
	AttemptSHA256 string `json:"attempt_sha256"`
	ResultSHA256  string `json:"result_sha256"`
	ApprovalCode  string `json:"approval_code"`
}

type Admission struct {
	Schema              string           `json:"schema"`
	Command             AdmissionCommand `json:"command"`
	ArticleSHA256       string           `json:"article_sha256"`
	ArtifactSHA256      string           `json:"artifact_sha256"`
	ContractSHA256      string           `json:"contract_sha256"`
	NewsReceipt         newscase.Receipt `json:"news_receipt"`
	Authority           string           `json:"authority"`
	QualificationCredit bool             `json:"qualification_credit"`
	SHA256              string           `json:"sha256"`
}

func PrepareAdmission(command AdmissionCommand, a Attempt) (news.Recording, error) {
	if a.Validate() != nil || !receiverID.MatchString(command.ReceiverID) || !ID.MatchString(command.AttemptID) || !sha.MatchString(command.AttemptSHA256) || !sha.MatchString(command.ResultSHA256) || command.ApprovalCode != ApprovalCode {
		return news.Recording{}, ErrInvalid
	}
	if command.ReceiverID != a.ReceiverID || command.AttemptID != a.ID || command.AttemptSHA256 != a.SHA256 || a.Status != "completed" || a.Result == nil || a.Result.Outcome != "extracted" || command.ResultSHA256 != a.Result.SHA256 {
		return news.Recording{}, ErrConflict
	}
	s := a.Article
	r := news.Recording{Schema: news.RecordingSchema, SourceID: s.SourceID, ArticleID: s.ArticleID, Revision: s.Revision, Kind: s.Kind, Supersedes: s.Supersedes, Publisher: s.Publisher, URL: s.URL, PublishedAt: s.PublishedAt, AvailableAt: s.AvailableAt, ReceivedAt: s.ReceivedAt, EvidenceClass: s.EvidenceClass, AccessRef: s.AccessRef, RawRef: s.RawRef, Original: s.Original, OriginalSHA256: s.OriginalSHA256, ExtractionMethod: news.ReviewedExtractionMethod, Claims: copyJSON(a.Result.Claims), Extraction: &news.ExtractionProvenance{Schema: news.ExtractionProvenanceSchema, ReceiverID: a.ReceiverID, AttemptID: a.ID, AttemptSHA256: a.SHA256, ResultSHA256: a.Result.SHA256, ArticleSHA256: a.ArticleSHA256, ArtifactSHA256: a.Result.Artifact.SHA256, ContractSHA256: a.ContractSHA256}}
	if _, err := news.Normalize(r); err != nil {
		return news.Recording{}, ErrInvalid
	}
	return copyJSON(r), nil
}

// MakeAdmission binds the approval to the authoritative attempt and the exact
// original/result/receipt committed by news admission in the same transaction.
func MakeAdmission(command AdmissionCommand, a Attempt, record newscase.Record) (Admission, error) {
	r, err := PrepareAdmission(command, a)
	if err != nil {
		return Admission{}, err
	}
	normalized, err := news.Normalize(r)
	if err != nil || !reflect.DeepEqual(record.Recording, r) || !reflect.DeepEqual(record.Result, normalized) || record.Receipt.RecordKey != newscase.RecordKey(r) || record.Receipt.RecordingSHA256 != normalized.RecordingSHA256 || record.Receipt.Duplicate || !validAt(record.Receipt.AcceptedAt) || record.Receipt.AcceptedAt.Before(a.CompletedAt) || len(record.Receipt.Matches) < 1 || len(record.Receipt.Matches) > maxAdmissionMatches {
		return Admission{}, ErrInvalid
	}
	seen := map[string]bool{}
	for _, m := range record.Receipt.Matches {
		if !strings.HasPrefix(m.CaseID, "news-case-") || !sha.MatchString(strings.TrimPrefix(m.CaseID, "news-case-")) || seen[m.CaseID] || m.Version < 1 || (m.Transition != "opened" && m.Transition != "updated" && m.Transition != "reopened") {
			return Admission{}, ErrInvalid
		}
		seen[m.CaseID] = true
	}
	// A correction can also revisit its predecessor's event, but the approved
	// operator event must always be among the affected case identities.
	if !seen[newscase.CaseID(r.EvidenceClass, a.EventKey)] {
		return Admission{}, ErrInvalid
	}
	approval := Admission{Schema: AdmissionSchema, Command: command, ArticleSHA256: a.ArticleSHA256, ArtifactSHA256: a.Result.Artifact.SHA256, ContractSHA256: a.ContractSHA256, NewsReceipt: copyJSON(record.Receipt), Authority: "reviewed_recorded_claims_only"}
	approval.SHA256 = hash(approval)
	return approval, nil
}

func (r Admission) Validate(a Attempt, record newscase.Record) error {
	expected, err := MakeAdmission(r.Command, a, record)
	if err != nil || !reflect.DeepEqual(r, expected) {
		return ErrInvalid
	}
	return nil
}

// ValidateMetadata is used by a CLI receipt reader before targeted source and
// attempt inspection. Hash integrity alone does not verify retained provenance.
func (r Admission) ValidateMetadata() error {
	if r.Schema != AdmissionSchema || r.Command.ApprovalCode != ApprovalCode || !receiverID.MatchString(r.Command.ReceiverID) || !ID.MatchString(r.Command.AttemptID) || r.Authority != "reviewed_recorded_claims_only" || r.QualificationCredit || r.NewsReceipt.Duplicate || !validAt(r.NewsReceipt.AcceptedAt) || len(r.NewsReceipt.Matches) < 1 || len(r.NewsReceipt.Matches) > maxAdmissionMatches {
		return ErrInvalid
	}
	for _, s := range []string{r.Command.AttemptSHA256, r.Command.ResultSHA256, r.ArticleSHA256, r.ArtifactSHA256, r.ContractSHA256, r.NewsReceipt.RecordKey, r.NewsReceipt.RecordingSHA256, r.SHA256} {
		if !sha.MatchString(s) {
			return ErrInvalid
		}
	}
	expected := r.SHA256
	r.SHA256 = ""
	if expected != hash(r) {
		return ErrInvalid
	}
	return nil
}
