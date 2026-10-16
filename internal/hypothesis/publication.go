package hypothesis

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"reflect"
	"regexp"
	"slices"
	"time"
	"unicode/utf8"

	"git.subcult.tv/subculture-collective/mantik/internal/investigation"
	"git.subcult.tv/subculture-collective/mantik/internal/news"
	"git.subcult.tv/subculture-collective/mantik/internal/newscase"
	"git.subcult.tv/subculture-collective/mantik/internal/review"
)

const PublicationSchema = "mantik-hypothesis-publication-v1"
const QuestionSchema = "mantik-retained-hypothesis-v1"
const MaxPublications = 32

var QuestionID = regexp.MustCompile(`^hypothesis-[a-f0-9]{64}$`)
var ErrConflict = errors.New("hypothesis publication identity, evidence or clock conflict")
var ErrNotFound = errors.New("retained hypothesis not found")
var ErrCapacity = errors.New("retained hypothesis publication capacity reached")

type Publication struct {
	Schema                string                `json:"schema"`
	ID                    string                `json:"id"`
	ReceiverID            string                `json:"receiver_id"`
	ReviewID              string                `json:"review_id"`
	ReviewResultID        string                `json:"review_result_id"`
	Subject               investigation.Subject `json:"subject"`
	PublishedAt           time.Time             `json:"published_at"`
	QuestionReused        bool                  `json:"question_reused"`
	OriginBindingVerified bool                  `json:"origin_binding_verified"`
	Sealed                Result                `json:"sealed"`
}
type Question struct {
	Schema              string        `json:"schema"`
	ID                  string        `json:"id"`
	Authority           string        `json:"authority"`
	QualificationCredit bool          `json:"qualification_credit"`
	Publications        []Publication `json:"publications"`
}

func SamePublication(p Publication, receiver, reviewID string, sealed Result) bool {
	return p.ReceiverID == receiver && p.ReviewID == reviewID && p.Sealed.DeclarationSHA256 == sealed.DeclarationSHA256
}

// Publish consumes an authoritative retained attempt and its current news state.
// It verifies source identity/binding, not truth, entailment or research validity.
// Storage serializes this check with case writes and retains the result atomically.
func Publish(s newscase.State, a review.Attempt, receiver, id string, d Declaration, now time.Time) (Publication, error) {
	sealed, err := Seal(d)
	if err != nil || !review.ID.MatchString(id) {
		return Publication{}, ErrInvalid
	}
	if !review.Inspect(a, s).ReviewApplicable || a.Schema != review.Schema || a.ModelMode != "recorded_model_fixture" || a.Pin.Subject.ReceiverID != receiver || a.CompletedAt.IsZero() || a.CompletedAt.Before(a.StartedAt) || !a.CompletedAt.Before(a.Deadline) || now.IsZero() || now.UTC().Year() < 1 || now.UTC().Year() > 9999 || now.Before(a.CompletedAt) || now.Before(s.LastClock) {
		return Publication{}, ErrConflict
	}
	group, err := newscase.ReviewGroup(s, a.Pin.Subject.CaseID, receiver)
	if err != nil || !reflect.DeepEqual(group, a.Pin.Group) {
		return Publication{}, ErrConflict
	}
	raw, _ := json.Marshal(group)
	if a.Pin.Subject.SnapshotSHA256 != news.SHA256(string(raw)) || a.Pin.Subject.EvidenceClass != sealed.Declaration.EvidenceClass || a.Pin.Subject.EvidenceClass != group.EvidenceClass || a.Pin.Subject.EventKey != group.EventKey || a.Pin.Subject.EventGroupID != group.ID || !a.Pin.Subject.AsOf.Equal(a.StartedAt) {
		return Publication{}, ErrConflict
	}
	r := *a.Result
	if !review.ResultMatchesProfile(a, r) || r.Status != "finalized" || r.Outcome != "propose_hypothesis" || r.Proposal == nil || r.Proposal.Outcome != r.Outcome || r.Proposal.Uncertainty != "unverified" || !reflect.DeepEqual(r.Subject, a.Pin.Subject) {
		return Publication{}, ErrConflict
	}
	resultID := r.ID
	r.ID = ""
	raw, _ = json.Marshal(r)
	if resultID != "review-"+news.SHA256(string(raw)) {
		return Publication{}, ErrConflict
	}
	contract, err := review.Definition(a)
	if err != nil {
		return Publication{}, ErrConflict
	}
	required := contract.RequiredChecks
	if len(r.Checks) != len(required) {
		return Publication{}, ErrConflict
	}
	for i, c := range r.Checks {
		if c.ID != required[i] || c.Version != "v1" || c.Status != "passed" || !c.Passed {
			return Publication{}, ErrConflict
		}
	}
	c := r.Proposal.Candidate
	if c == nil || c.Kind != "research_hypothesis" || c.ClaimCode != d.Mechanism.Code || c.RefutationCode != d.Refutation.Code || len(c.EvidenceKeys) != len(d.Origins) || len(c.EvidenceKeys) != len(r.Proposal.ClaimKeys) || len(c.EvidenceKeys) < 1 || len(c.EvidenceKeys) > 4 {
		return Publication{}, ErrConflict
	}
	if contract.TypeID == investigation.MethodTypeID {
		if len(c.Methods) != len(sealed.Declaration.Methods) {
			return Publication{}, ErrConflict
		}
		seen := map[string]bool{}
		for _, ref := range c.Methods {
			if seen[ref.ID] || !slices.Contains(sealed.Declaration.Methods, ref) {
				return Publication{}, ErrConflict
			}
			seen[ref.ID] = true
			_, method, err := PinMethod(ref.ID)
			if err != nil {
				return Publication{}, ErrConflict
			}
			inspected := false
			for _, tool := range r.Tools {
				if tool.Name == "inspect_native_method" && tool.Status == "ok" && tool.MethodRef != nil && *tool.MethodRef == ref && tool.Method != nil && reflect.DeepEqual(*tool.Method, method) {
					inspected = true
				}
			}
			if !inspected {
				return Publication{}, ErrConflict
			}
		}
	}

	keys := map[string]bool{}
	for _, key := range c.EvidenceKeys {
		if keys[key] || !slices.Contains(r.Proposal.ClaimKeys, key) {
			return Publication{}, ErrConflict
		}
		keys[key] = true
	}
	for _, origin := range sealed.Declaration.Origins {
		if !keys[origin.ClaimKey] {
			return Publication{}, ErrConflict
		}
		var bound *news.EventClaim
		for i := range group.Claims {
			if group.Claims[i].Claim.Key == origin.ClaimKey {
				bound = &group.Claims[i]
				break
			}
		}
		if bound == nil || bound.State != "current" || bound.Claim.Assessment != "reported_unverified" || bound.Claim.EvidenceClass != d.EvidenceClass || !slices.Contains(bound.Claim.Symbols, d.Instrument) || bound.Reference != origin.Reference || bound.Claim.RecordingSHA256 != origin.RecordingSHA256 {
			return Publication{}, ErrConflict
		}
		inspected := false
		for _, tool := range r.Tools {
			if tool.Name == "inspect_news_claim" && tool.Status == "ok" && tool.State == bound.State && tool.Reference == bound.Reference && tool.RecordKey == bound.RecordKey && tool.Claim != nil && reflect.DeepEqual(*tool.Claim, bound.Claim) {
				inspected = true
			}
		}
		if !inspected {
			return Publication{}, ErrConflict
		}
	}
	return Publication{Schema: PublicationSchema, ID: id, ReceiverID: receiver, ReviewID: a.ID, ReviewResultID: resultID, Subject: a.Pin.Subject, PublishedAt: now.UTC(), OriginBindingVerified: true, Sealed: sealed}, nil
}

// PublicationCommand carries operator input only; pins and receipts are server-owned.
type PublicationCommand struct {
	ReceiverID  string      `json:"receiver_id"`
	RequestID   string      `json:"request_id"`
	ReviewID    string      `json:"review_id"`
	Declaration Declaration `json:"declaration"`
}

func ReadPublicationCommand(reader io.Reader) (PublicationCommand, error) {
	raw, err := io.ReadAll(io.LimitReader(reader, MaxBytes+4097))
	if err != nil || len(raw) > MaxBytes+4096 || !utf8.Valid(raw) {
		return PublicationCommand{}, ErrInvalid
	}
	if err = unique(json.NewDecoder(bytes.NewReader(raw)), 0); err != nil {
		return PublicationCommand{}, err
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var c PublicationCommand
	if decoder.Decode(&c) != nil || decoder.Decode(new(any)) != io.EOF {
		return c, ErrInvalid
	}
	dRaw, _ := json.Marshal(c.Declaration)
	if len(dRaw) > MaxBytes || !review.ID.MatchString(c.RequestID) || !review.ID.MatchString(c.ReviewID) {
		return c, ErrInvalid
	}
	_, err = Seal(c.Declaration)
	return c, err
}
