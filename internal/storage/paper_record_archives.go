package storage

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"slices"
	"strings"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/allocation"
	"git.subcult.tv/subculture-collective/mantik/internal/paperaccount"
	"git.subcult.tv/subculture-collective/mantik/internal/paperstrategy"
	"git.subcult.tv/subculture-collective/mantik/internal/socialwatch"
	"git.subcult.tv/subculture-collective/mantik/internal/workerplan"
)

const MaxPaperRecordArchiveBytes = 64 << 20
const maxPaperArchiveRecords = MaxPaperDecisions + MaxPaperOutcomes + MaxPaperEvaluationAssessments + MaxDecisionReviews

var ErrPaperRecordArchiveInvalid = errors.New("invalid paper record archive request")
var ErrPaperRecordArchiveConflict = errors.New("paper record archive owner or source conflict")
var ErrPaperRecordArchiveCapacity = errors.New("paper record archive capacity exhausted")
var ErrPaperRecordArchiveNotFound = errors.New("paper record archive not found")

type PaperRecordArchiveHeader struct {
	Schema              string    `json:"schema"`
	ReceiverID          string    `json:"receiver_id"`
	Sequence            int64     `json:"sequence"`
	ArchivedAt          time.Time `json:"archived_at"`
	PreviousSHA256      string    `json:"previous_sha256"`
	SourceSHA256        string    `json:"source_sha256"`
	RecordsSHA256       string    `json:"records_sha256"`
	Decisions           int       `json:"decisions"`
	Outcomes            int       `json:"outcomes"`
	Assessments         int       `json:"assessments"`
	Reviews             int       `json:"reviews"`
	Authority           string    `json:"authority"`
	AdmissionAvailable  bool      `json:"admission_available"`
	ExecutionAvailable  bool      `json:"execution_available"`
	QualificationCredit bool      `json:"qualification_credit"`
	SHA256              string    `json:"sha256"`
}

type PaperArchiveRecordPin struct {
	PaperRecordEpoch
	PayloadSHA256 string `json:"payload_sha256"`
}

type ArchivedPaperRecord struct {
	PaperArchiveRecordPin
	Payload json.RawMessage `json:"payload"`
}

// Context is the original owner snapshot used to replay the archived records.
// These are retained source assertions, never fresh grants or provider proof.
type PaperArchiveContext struct {
	Feed             FeedEvidenceSnapshot              `json:"feed"`
	Strategies       paperstrategy.Catalog             `json:"strategies"`
	Policies         allocation.PolicyCatalog          `json:"policies"`
	Declarations     PaperEvaluationDeclarationCatalog `json:"declarations"`
	CorporateSources []CorporateActionSourceRecord     `json:"corporate_sources"`
	ReferenceSources []CorporateReferenceSourceRecord  `json:"reference_sources"`
	SocialSources    []socialwatch.Journal             `json:"social_sources"`
}

type PaperRecordArchive struct {
	PaperRecordArchiveHeader
	Context PaperArchiveContext   `json:"context"`
	Records []ArchivedPaperRecord `json:"records"`
}

type PaperRecordArchiveReceipt struct {
	Header    PaperRecordArchiveHeader `json:"header"`
	Duplicate bool                     `json:"duplicate"`
}

type PaperRecordArchivePage struct {
	ReceiverID string                     `json:"receiver_id"`
	Records    []PaperRecordArchiveHeader `json:"records"`
	NextAfter  int64                      `json:"next_after,omitempty"`
}

type PaperArchivedRecord struct {
	Archive PaperRecordArchiveHeader `json:"archive"`
	Record  ArchivedPaperRecord      `json:"record"`
}

type PaperRecordArchiveStore interface {
	ArchivePaperRecords(context.Context, string, func() time.Time, PaperDecisionEvaluator, PaperOutcomeEvaluator) (PaperRecordArchiveReceipt, error)
	PaperRecordArchive(context.Context, int64, PaperDecisionEvaluator, PaperOutcomeEvaluator) (PaperRecordArchive, error)
	PaperRecordArchives(context.Context, int64, int) (PaperRecordArchivePage, error)
	PaperArchivedRecord(context.Context, string, string, PaperDecisionEvaluator, PaperOutcomeEvaluator) (PaperArchivedRecord, error)
}

func (h PaperRecordArchiveHeader) Validate() error {
	sha := h.SHA256
	h.SHA256 = ""
	if h.Schema != "mantik-paper-record-archive-v1" || !ValidReceiverID(h.ReceiverID) || h.Sequence < 1 || !declarationTime(h.ArchivedAt) ||
		h.Sequence == 1 && h.PreviousSHA256 != "" || h.Sequence > 1 && !decisionPin.MatchString(h.PreviousSHA256) ||
		!decisionPin.MatchString(h.SourceSHA256) || !decisionPin.MatchString(h.RecordsSHA256) ||
		h.Decisions < 0 || h.Decisions > MaxPaperDecisions || h.Outcomes < 0 || h.Outcomes > MaxPaperOutcomes ||
		h.Assessments < 0 || h.Assessments > MaxPaperEvaluationAssessments || h.Reviews < 0 || h.Reviews > MaxDecisionReviews ||
		h.Authority != "service_owned_paper_record_custody_only" || h.AdmissionAvailable || h.ExecutionAvailable || h.QualificationCredit || workerplan.Hash(h) != sha {
		return ErrPaperRecordArchiveConflict
	}
	return nil
}

func paperRecordKind(kind string) bool {
	return kind == "decision" || kind == "outcome" || kind == "assessment" || kind == "review"
}

func paperArchivePins(records []ArchivedPaperRecord) []PaperArchiveRecordPin {
	result := make([]PaperArchiveRecordPin, len(records))
	for i, r := range records {
		result[i] = r.PaperArchiveRecordPin
	}
	return result
}

func (a PaperRecordArchive) Validate() error {
	h := a.PaperRecordArchiveHeader
	if h.Validate() != nil || a.Records == nil || len(a.Records) > maxPaperArchiveRecords || a.Context.Feed.ReceiverID != h.ReceiverID ||
		workerplan.Hash(paperArchivePins(a.Records)) != h.RecordsSHA256 || workerplan.Hash(struct {
		Context PaperArchiveContext
		Records []ArchivedPaperRecord
	}{a.Context, a.Records}) != h.SourceSHA256 {
		return ErrPaperRecordArchiveConflict
	}
	previous := ""
	counts := map[string]int{}
	for _, r := range a.Records {
		key := r.Kind + ":" + r.ID
		if !paperRecordKind(r.Kind) || !paperaccount.ID.MatchString(r.ID) || key <= previous || r.CreatedEpoch < 0 || r.CreatedEpoch >= h.Sequence || !decisionPin.MatchString(r.RecordSHA256) || !declarationTime(r.At) || r.At.After(h.ArchivedAt) || workerplan.Hash(r.Payload) != r.PayloadSHA256 {
			return ErrPaperRecordArchiveConflict
		}
		previous = key
		counts[r.Kind]++
	}
	if counts["decision"] != h.Decisions || counts["outcome"] != h.Outcomes || counts["assessment"] != h.Assessments || counts["review"] != h.Reviews {
		return ErrPaperRecordArchiveConflict
	}
	return nil
}

type paperArchiveOwners struct {
	decisions   map[string]retainedPaperDecision
	outcomes    map[string]retainedPaperOutcome
	assessments map[string]PaperEvaluationAssessment
	reviews     map[string]DecisionReviewRecord
}

func (o paperArchiveOwners) keys() []string {
	keys := make([]string, 0, len(o.decisions)+len(o.outcomes)+len(o.assessments)+len(o.reviews))
	for id := range o.decisions {
		keys = append(keys, "decision:"+id)
	}
	for id := range o.outcomes {
		keys = append(keys, "outcome:"+id)
	}
	for id := range o.assessments {
		keys = append(keys, "assessment:"+id)
	}
	for id := range o.reviews {
		keys = append(keys, "review:"+id)
	}
	slices.Sort(keys)
	return keys
}

func (o paperArchiveOwners) records(epochs map[string]PaperRecordEpoch) ([]ArchivedPaperRecord, error) {
	out := make([]ArchivedPaperRecord, 0, len(o.keys()))
	add := func(kind, id, sha string, at time.Time, value any) error {
		e, ok := epochs[kind+":"+id]
		if !ok || e.Kind != kind || e.ID != id || e.RecordSHA256 != sha || !e.At.Equal(at) {
			return ErrPaperRecordArchiveConflict
		}
		raw, err := json.Marshal(value)
		if err != nil {
			return err
		}
		out = append(out, ArchivedPaperRecord{PaperArchiveRecordPin{e, workerplan.Hash(json.RawMessage(raw))}, raw})
		return nil
	}
	for id, v := range o.decisions {
		if err := add("decision", id, v.Record.SHA256, v.Record.FinalizedAt, v); err != nil {
			return nil, err
		}
	}
	for id, v := range o.outcomes {
		if err := add("outcome", id, v.Record.SHA256, v.Record.EvaluatedAt, v); err != nil {
			return nil, err
		}
	}
	for id, v := range o.assessments {
		if err := add("assessment", id, v.SHA256, v.AssessedAt, v); err != nil {
			return nil, err
		}
	}
	for id, v := range o.reviews {
		if err := add("review", id, v.SHA256, v.ReviewedAt, v); err != nil {
			return nil, err
		}
	}
	slices.SortFunc(out, func(a, b ArchivedPaperRecord) int { return strings.Compare(a.Kind+":"+a.ID, b.Kind+":"+b.ID) })
	return out, nil
}

func decodePaperArchiveOwners(records []ArchivedPaperRecord) (paperArchiveOwners, error) {
	o := paperArchiveOwners{map[string]retainedPaperDecision{}, map[string]retainedPaperOutcome{}, map[string]PaperEvaluationAssessment{}, map[string]DecisionReviewRecord{}}
	for _, p := range records {
		var id, sha string
		var at time.Time
		switch p.Kind {
		case "decision":
			var v retainedPaperDecision
			if workerplan.ReadBoundedShape(p.Payload, &v, MaxPaperDecisionBytes) != nil {
				return o, ErrPaperRecordArchiveConflict
			}
			id, sha, at = v.Record.Command.ID, v.Record.SHA256, v.Record.FinalizedAt
			o.decisions[id] = v
		case "outcome":
			var v retainedPaperOutcome
			if workerplan.ReadBoundedShape(p.Payload, &v, MaxPaperOutcomeBytes) != nil {
				return o, ErrPaperRecordArchiveConflict
			}
			id, sha, at = v.Record.Command.ID, v.Record.SHA256, v.Record.EvaluatedAt
			o.outcomes[id] = v
		case "assessment":
			var v PaperEvaluationAssessment
			if workerplan.ReadBoundedShape(p.Payload, &v, MaxPaperEvaluationAssessmentBytes) != nil {
				return o, ErrPaperRecordArchiveConflict
			}
			id, sha, at = v.Command.ID, v.SHA256, v.AssessedAt
			o.assessments[id] = v
		case "review":
			var v DecisionReviewRecord
			if workerplan.ReadBoundedShape(p.Payload, &v, MaxDecisionReviewBytes) != nil {
				return o, ErrPaperRecordArchiveConflict
			}
			id, sha, at = v.Command.ID, v.SHA256, v.ReviewedAt
			o.reviews[id] = v
		default:
			return o, ErrPaperRecordArchiveConflict
		}
		if id != p.ID || sha != p.RecordSHA256 || !at.Equal(p.At) {
			return o, ErrPaperRecordArchiveConflict
		}
	}
	return o, nil
}

func paperArchiveContext(s paperEvaluationSources, social []socialwatch.Journal) PaperArchiveContext {
	return PaperArchiveContext{s.feed, s.strategies, s.policies, s.declarations, s.corporateSources, s.referenceSources, social}
}

func paperArchiveEvaluationSources(ctx context.Context, c PaperArchiveContext, o paperArchiveOwners, de PaperDecisionEvaluator, oe PaperOutcomeEvaluator, portfolio PaperPortfolioEvaluator) (paperEvaluationSources, error) {
	owner := c.Feed.ReceiverID
	if de == nil || oe == nil || c.Strategies.Validate() != nil || c.Policies.Validate() != nil || c.Strategies.ReceiverID != owner || c.Policies.ReceiverID != owner {
		return paperEvaluationSources{}, ErrPaperRecordArchiveConflict
	}
	decisions, err := paperDecisionCatalog(owner, o.decisions, c.Strategies, c.Feed, de)
	if err != nil {
		return paperEvaluationSources{}, err
	}
	outcomes, err := paperOutcomeCatalog(owner, o.outcomes, o.decisions, c.Strategies, c.Feed, de, oe)
	if err != nil {
		return paperEvaluationSources{}, err
	}
	plans := map[string]PaperEvaluationDeclaration{}
	for _, r := range c.Declarations.Records {
		if _, exists := plans[r.Command.ID]; exists {
			return paperEvaluationSources{}, ErrPaperRecordArchiveConflict
		}
		plans[r.Command.ID] = r
	}
	actual, err := paperEvaluationDeclarationCatalog(owner, plans, c.Strategies, c.Policies)
	if err != nil || !reflect.DeepEqual(actual, c.Declarations) {
		return paperEvaluationSources{}, ErrPaperRecordArchiveConflict
	}
	return paperEvaluationSources{feed: c.Feed, policies: c.Policies, retainedOutcomes: o.outcomes, portfolioEvaluator: portfolio, corporateSources: c.CorporateSources, referenceSources: c.ReferenceSources, declarations: c.Declarations, decisions: decisions, outcomes: outcomes, retained: o.decisions, sourceClock: c.Feed.Collectors.LastClock, strategies: c.Strategies}, nil
}

func verifyPaperArchiveOwners(ctx context.Context, c PaperArchiveContext, o paperArchiveOwners, de PaperDecisionEvaluator, oe PaperOutcomeEvaluator, portfolio PaperPortfolioEvaluator, reader archivedFeedWindowReader) error {
	owner := c.Feed.ReceiverID
	s, err := paperArchiveEvaluationSources(ctx, c, o, de, oe, portfolio)
	if err != nil {
		return err
	}
	if _, err := paperEvaluationAssessmentCatalog(ctx, owner, o.assessments, s); err != nil {
		return err
	}
	if _, err := decisionReviewCatalog(owner, o.reviews, s.decisions, c.SocialSources...); err != nil {
		return err
	}
	return auditOriginalPaperFeed(ctx, c.Feed, o.decisions, o.outcomes, c.Strategies, &paperFeedArchiveEvaluators{de, oe}, time.Time{}, reader)
}

func newPaperRecordArchive(c PaperArchiveContext, records []ArchivedPaperRecord, previous PaperRecordArchiveHeader, now func() time.Time) (PaperRecordArchive, error) {
	if now == nil {
		return PaperRecordArchive{}, ErrPaperRecordArchiveInvalid
	}
	at := now().UTC()
	if !declarationTime(at) || at.Before(c.Feed.Collectors.LastClock) || previous.Sequence > 0 && (previous.Validate() != nil || at.Before(previous.ArchivedAt)) {
		return PaperRecordArchive{}, ErrPaperRecordArchiveConflict
	}
	a := PaperRecordArchive{PaperRecordArchiveHeader: PaperRecordArchiveHeader{Schema: "mantik-paper-record-archive-v1", ReceiverID: c.Feed.ReceiverID, Sequence: previous.Sequence + 1, ArchivedAt: at, PreviousSHA256: previous.SHA256, Authority: "service_owned_paper_record_custody_only"}, Context: c, Records: records}
	for _, r := range records {
		switch r.Kind {
		case "decision":
			a.Decisions++
		case "outcome":
			a.Outcomes++
		case "assessment":
			a.Assessments++
		case "review":
			a.Reviews++
		}
	}
	a.RecordsSHA256 = workerplan.Hash(paperArchivePins(records))
	a.SourceSHA256 = workerplan.Hash(struct {
		Context PaperArchiveContext
		Records []ArchivedPaperRecord
	}{c, records})
	a.SHA256 = workerplan.Hash(a.PaperRecordArchiveHeader)
	if a.Validate() != nil {
		return PaperRecordArchive{}, ErrPaperRecordArchiveConflict
	}
	raw, err := json.Marshal(a)
	if err != nil || len(raw) > MaxPaperRecordArchiveBytes {
		return PaperRecordArchive{}, ErrPaperRecordArchiveCapacity
	}
	return a, nil
}

func paperArchiveExtends(old PaperRecordArchive, records []ArchivedPaperRecord) error {
	byID := map[string]ArchivedPaperRecord{}
	for _, r := range records {
		byID[r.Kind+":"+r.ID] = r
	}
	for _, r := range old.Records {
		v, ok := byID[r.Kind+":"+r.ID]
		if !ok || !reflect.DeepEqual(v, r) {
			return ErrPaperRecordArchiveConflict
		}
	}
	return nil
}
