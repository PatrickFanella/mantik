package storage

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"slices"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/domain"
	"git.subcult.tv/subculture-collective/mantik/internal/paperaccount"
	"git.subcult.tv/subculture-collective/mantik/internal/workerplan"
	"github.com/jackc/pgx/v5"
)

const MaxOwnedAdmissionReviews = 128
const MaxOwnedAdmissionReviewBytes = 32768

var ErrOwnedAdmissionReviewInvalid = errors.New("invalid owned admission review")
var ErrOwnedAdmissionReviewConflict = errors.New("owned admission review identity or predecessor conflict")
var ErrOwnedAdmissionReviewNotFound = errors.New("owned admission review source not found")
var ErrOwnedAdmissionReviewCapacity = errors.New("owned admission review capacity exhausted")

// Only actual owner identities and the exact predecessor are public inputs.
// A review never accepts a supplied admission, account, result or financial input.
type OwnedAdmissionReviewCommand struct {
	ID               string `json:"id"`
	ReceiverID       string `json:"receiver_id"`
	ComparisonID     string `json:"comparison_id"`
	ComparisonSHA256 string `json:"comparison_sha256"`
	DecisionID       string `json:"decision_id"`
	DecisionSHA256   string `json:"decision_sha256"`
	PreviousID       string `json:"previous_id"`
	PreviousSHA256   string `json:"previous_sha256"`
}

func (c OwnedAdmissionReviewCommand) Validate() error {
	if !paperaccount.ID.MatchString(c.ID) || !ValidReceiverID(c.ReceiverID) || !paperaccount.ID.MatchString(c.ComparisonID) || !decisionPin.MatchString(c.ComparisonSHA256) || !paperaccount.ID.MatchString(c.DecisionID) || !decisionPin.MatchString(c.DecisionSHA256) || (c.PreviousID == "") != (c.PreviousSHA256 == "") || c.PreviousID != "" && (!paperaccount.ID.MatchString(c.PreviousID) || !decisionPin.MatchString(c.PreviousSHA256)) {
		return ErrOwnedAdmissionReviewInvalid
	}
	return nil
}

type OwnedAdmissionReview struct {
	Schema              string                      `json:"schema"`
	Method              string                      `json:"method"`
	Command             OwnedAdmissionReviewCommand `json:"command"`
	Sequence            int                         `json:"sequence"`
	ReviewedAt          time.Time                   `json:"reviewed_at"`
	ComparisonAt        time.Time                   `json:"comparison_at"`
	Readiness           OwnedAdmissionReadiness     `json:"readiness"`
	Status              string                      `json:"status"`
	ReasonCode          string                      `json:"reason_code"`
	ReconsiderTriggers  []string                    `json:"reconsider_triggers"`
	Authority           string                      `json:"authority"`
	AdmissionAvailable  bool                        `json:"admission_available"`
	ExecutionAvailable  bool                        `json:"execution_available"`
	QualificationCredit bool                        `json:"qualification_credit"`
	SHA256              string                      `json:"sha256"`
}
type OwnedAdmissionReviewReceipt struct {
	Record    OwnedAdmissionReview `json:"record"`
	Duplicate bool                 `json:"duplicate"`
}
type OwnedAdmissionReviewCatalog struct {
	ReceiverID string                 `json:"receiver_id"`
	Records    []OwnedAdmissionReview `json:"records"`
	CurrentIDs []string               `json:"current_ids"`
}
type OwnedAdmissionReviewStore interface {
	ReviewOwnedAdmission(context.Context, OwnedAdmissionReviewCommand, func() time.Time, PaperDecisionEvaluator, PaperOutcomeEvaluator) (OwnedAdmissionReviewReceipt, error)
	OwnedAdmissionReviews(context.Context, PaperDecisionEvaluator, PaperOutcomeEvaluator) (OwnedAdmissionReviewCatalog, error)
}

// Comparisons have already been replayed from their actual decision, policy,
// declaration and assessment owners in the same lock or transaction.
func newOwnedAdmissionReview(c OwnedAdmissionReviewCommand, source OwnedAllocationCatalog, sequence int, at time.Time) (OwnedAdmissionReview, error) {
	if c.Validate() != nil || c.ReceiverID != source.ReceiverID || !declarationTime(at) || sequence < 1 {
		return OwnedAdmissionReview{}, ErrOwnedAdmissionReviewInvalid
	}
	var comparison *OwnedAllocationRecord
	for i := range source.Records {
		if source.Records[i].Command.ID == c.ComparisonID {
			comparison = &source.Records[i]
			break
		}
	}
	if comparison == nil {
		return OwnedAdmissionReview{}, ErrOwnedAdmissionReviewNotFound
	}
	if comparison.SHA256 != c.ComparisonSHA256 {
		return OwnedAdmissionReview{}, ErrOwnedAdmissionReviewConflict
	}
	if at.Before(comparison.At) {
		return OwnedAdmissionReview{}, ErrOwnedAdmissionReviewInvalid
	}
	var readiness *OwnedAdmissionReadiness
	for i := range comparison.AdmissionChecks {
		v := &comparison.AdmissionChecks[i]
		if v.DecisionID == c.DecisionID && v.DecisionSHA256 == c.DecisionSHA256 {
			readiness = v
			break
		}
	}
	if readiness == nil {
		return OwnedAdmissionReview{}, ErrOwnedAdmissionReviewNotFound
	}
	if (readiness.Schema != "mantik-owned-admission-readiness-v3" && readiness.Schema != "mantik-owned-admission-readiness-v4" && readiness.Schema != "mantik-owned-admission-readiness-v5" && readiness.Schema != "mantik-owned-admission-readiness-v6") || readiness.Binding == nil || readiness.AdmissionAvailable || readiness.ExecutionAvailable || readiness.QualificationCredit {
		return OwnedAdmissionReview{}, ErrOwnedAdmissionReviewInvalid
	}
	r := OwnedAdmissionReview{Schema: "mantik-owned-admission-review-v1", Method: "retained-non-grant-admission-review-v1", Command: c, Sequence: sequence, ReviewedAt: at.UTC(), ComparisonAt: comparison.At, Readiness: clone(*readiness), Status: "deferred", ReasonCode: "required_admission_gates_unavailable", ReconsiderTriggers: clone(readiness.ReconsiderTriggers), Authority: "service_owned_non_grant_admission_review_only"}
	failed, pending := false, false
	for _, g := range readiness.Gates {
		failed = failed || g.Status == "failed"
		pending = pending || g.Status == "pending"
	}
	switch {
	case !at.Before(readiness.ValidUntil):
		r.Status = "expired"
		r.ReasonCode = "finalized_proposal_expired"
		r.ReconsiderTriggers = []string{"new_finalized_decision"}
	case failed:
		r.Status = "blocked"
		r.ReasonCode = "retained_necessary_evaluation_check_failed"
	case pending:
		r.Status = "pending"
		r.ReasonCode = "retained_evaluation_window_open"
	}
	r.SHA256 = workerplan.Hash(r)
	raw, err := json.Marshal(r)
	if err != nil || len(raw) > MaxOwnedAdmissionReviewBytes {
		return OwnedAdmissionReview{}, ErrOwnedAdmissionReviewCapacity
	}
	return r, nil
}
func ownedAdmissionReviewCatalog(ctx context.Context, owner string, all map[string]OwnedAdmissionReview, source OwnedAllocationCatalog) (OwnedAdmissionReviewCatalog, error) {
	out := OwnedAdmissionReviewCatalog{ReceiverID: owner, Records: []OwnedAdmissionReview{}, CurrentIDs: []string{}}
	if !ValidReceiverID(owner) || source.ReceiverID != owner || len(all) > MaxOwnedAdmissionReviews {
		return OwnedAdmissionReviewCatalog{}, ErrOwnedAdmissionReviewInvalid
	}
	for id, r := range all {
		if id != r.Command.ID {
			return OwnedAdmissionReviewCatalog{}, ErrOwnedAdmissionReviewInvalid
		}
		out.Records = append(out.Records, clone(r))
	}
	slices.SortFunc(out.Records, func(a, b OwnedAdmissionReview) int { return a.Sequence - b.Sequence })
	current := map[string]OwnedAdmissionReview{}
	var clock time.Time
	for i, r := range out.Records {
		if err := ctx.Err(); err != nil {
			return OwnedAdmissionReviewCatalog{}, err
		}
		want, err := newOwnedAdmissionReview(r.Command, source, i+1, r.ReviewedAt)
		previous := current[r.Command.DecisionID]
		if err != nil || r.Command.ReceiverID != owner || !reflect.DeepEqual(want, r) || r.Command.PreviousID != previous.Command.ID || r.Command.PreviousSHA256 != previous.SHA256 || r.ReviewedAt.Before(clock) {
			return OwnedAdmissionReviewCatalog{}, ErrOwnedAdmissionReviewInvalid
		}
		if previous.Command.ID != "" && (previous.Command.DecisionSHA256 != r.Command.DecisionSHA256 || r.ComparisonAt.Before(previous.ComparisonAt)) {
			return OwnedAdmissionReviewCatalog{}, ErrOwnedAdmissionReviewInvalid
		}
		current[r.Command.DecisionID] = r
		clock = r.ReviewedAt
	}
	for _, r := range current {
		out.CurrentIDs = append(out.CurrentIDs, r.Command.ID)
	}
	slices.Sort(out.CurrentIDs)
	return out, nil
}
func appendOwnedAdmissionReview(ctx context.Context, owner string, all map[string]OwnedAdmissionReview, source OwnedAllocationCatalog, c OwnedAdmissionReviewCommand, now func() time.Time, floor time.Time) (OwnedAdmissionReviewReceipt, error) {
	catalog, err := ownedAdmissionReviewCatalog(ctx, owner, all, source)
	if err != nil {
		return OwnedAdmissionReviewReceipt{}, err
	}
	if c.Validate() != nil {
		return OwnedAdmissionReviewReceipt{}, ErrOwnedAdmissionReviewInvalid
	}
	if c.ReceiverID != owner {
		return OwnedAdmissionReviewReceipt{}, ErrReceiverMismatch
	}
	if old, ok := all[c.ID]; ok {
		if old.Command != c {
			return OwnedAdmissionReviewReceipt{}, ErrOwnedAdmissionReviewConflict
		}
		return OwnedAdmissionReviewReceipt{clone(old), true}, nil
	}
	if len(all) >= MaxOwnedAdmissionReviews {
		return OwnedAdmissionReviewReceipt{}, ErrOwnedAdmissionReviewCapacity
	}
	var previous OwnedAdmissionReview
	for _, r := range catalog.Records {
		if r.Command.DecisionID == c.DecisionID {
			previous = r
		}
	}
	if c.PreviousID != previous.Command.ID || c.PreviousSHA256 != previous.SHA256 || previous.Command.ID != "" && previous.Command.DecisionSHA256 != c.DecisionSHA256 {
		return OwnedAdmissionReviewReceipt{}, ErrOwnedAdmissionReviewConflict
	}
	if now == nil {
		return OwnedAdmissionReviewReceipt{}, ErrOwnedAdmissionReviewInvalid
	}
	at := now()
	if at.Before(floor) || len(catalog.Records) > 0 && at.Before(catalog.Records[len(catalog.Records)-1].ReviewedAt) {
		return OwnedAdmissionReviewReceipt{}, ErrOwnedAdmissionReviewInvalid
	}
	r, err := newOwnedAdmissionReview(c, source, len(all)+1, at)
	if err != nil {
		return OwnedAdmissionReviewReceipt{}, err
	}
	if previous.Command.ID != "" && r.ComparisonAt.Before(previous.ComparisonAt) {
		return OwnedAdmissionReviewReceipt{}, ErrOwnedAdmissionReviewConflict
	}
	return OwnedAdmissionReviewReceipt{r, false}, nil
}
func (m *Memory) admissionReviewSourcesLocked(ctx context.Context, de PaperDecisionEvaluator, oe PaperOutcomeEvaluator) (OwnedAllocationCatalog, error) {
	if de == nil || oe == nil {
		return OwnedAllocationCatalog{}, ErrOwnedAdmissionReviewInvalid
	}
	p, d, s, err := m.ownedAllocationSourcesLocked(de)
	if err != nil {
		return OwnedAllocationCatalog{}, err
	}
	plans, err := paperEvaluationDeclarationCatalog(m.receiverID, m.paperEvaluationPlans, s, p)
	if err != nil {
		return OwnedAllocationCatalog{}, err
	}
	actual, err := resolveAllocationAssessmentOwner(ctx, m.receiverID, m.evaluationAssessments, plans, d, s, m.paperDecisions, m.paperOutcomes, m.decisionFeedLocked(), m.corporateActionSources, de, oe, p, m.portfolioEvaluator)
	if err != nil {
		return OwnedAllocationCatalog{}, err
	}
	return replayOwnedAllocations(ctx, m.receiverID, m.ownedAllocations, p, d, s, m.paperDecisions, plans, actual)
}
func (m *Memory) ReviewOwnedAdmission(ctx context.Context, c OwnedAdmissionReviewCommand, now func() time.Time, de PaperDecisionEvaluator, oe PaperOutcomeEvaluator) (OwnedAdmissionReviewReceipt, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return OwnedAdmissionReviewReceipt{}, err
	}
	source, err := m.admissionReviewSourcesLocked(ctx, de, oe)
	if err != nil {
		return OwnedAdmissionReviewReceipt{}, err
	}
	r, err := appendOwnedAdmissionReview(ctx, m.receiverID, m.admissionReviews, source, c, now, m.collectors.LastClock)
	if err != nil {
		return OwnedAdmissionReviewReceipt{}, err
	}
	if err = ctx.Err(); err != nil {
		return OwnedAdmissionReviewReceipt{}, err
	}
	if !r.Duplicate {
		m.admissionReviews[c.ID] = clone(r.Record)
	}
	return r, nil
}
func (m *Memory) OwnedAdmissionReviews(ctx context.Context, de PaperDecisionEvaluator, oe PaperOutcomeEvaluator) (OwnedAdmissionReviewCatalog, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return OwnedAdmissionReviewCatalog{}, err
	}
	source, err := m.admissionReviewSourcesLocked(ctx, de, oe)
	if err != nil {
		return OwnedAdmissionReviewCatalog{}, err
	}
	return ownedAdmissionReviewCatalog(ctx, m.receiverID, m.admissionReviews, source)
}
func readOwnedAdmissionReviews(ctx context.Context, tx pgx.Tx) (map[string]OwnedAdmissionReview, error) {
	rows, err := tx.Query(ctx, "SELECT id,payload FROM mantik_owned_admission_reviews ORDER BY id LIMIT $1", MaxOwnedAdmissionReviews+1)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	all := map[string]OwnedAdmissionReview{}
	for rows.Next() {
		var id string
		var raw []byte
		var r OwnedAdmissionReview
		if err = rows.Scan(&id, &raw); err != nil {
			return nil, err
		}
		if len(all) >= MaxOwnedAdmissionReviews || workerplan.ReadBoundedShape(raw, &r, MaxOwnedAdmissionReviewBytes) != nil || id != r.Command.ID {
			return nil, ErrOwnedAdmissionReviewInvalid
		}
		all[id] = r
	}
	return all, rows.Err()
}
func (p *Postgres) admissionReviewAccess(ctx context.Context, write bool, c OwnedAdmissionReviewCommand, now func() time.Time, de PaperDecisionEvaluator, oe PaperOutcomeEvaluator) (OwnedAdmissionReviewCatalog, OwnedAdmissionReviewReceipt, error) {
	if de == nil || oe == nil {
		return OwnedAdmissionReviewCatalog{}, OwnedAdmissionReviewReceipt{}, ErrOwnedAdmissionReviewInvalid
	}
	opts := pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly}
	if write {
		opts = pgx.TxOptions{}
	}
	tx, err := p.pool.BeginTx(ctx, opts)
	if err != nil {
		return OwnedAdmissionReviewCatalog{}, OwnedAdmissionReviewReceipt{}, err
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	s, err := readPaperEvaluationSources(ctx, tx, write, de, oe, p.portfolioEvaluator)
	if err != nil {
		return OwnedAdmissionReviewCatalog{}, OwnedAdmissionReviewReceipt{}, err
	}
	assessments, err := readPaperEvaluationAssessments(ctx, tx)
	if err != nil {
		return OwnedAdmissionReviewCatalog{}, OwnedAdmissionReviewReceipt{}, err
	}
	actual, err := paperEvaluationAssessmentCatalog(ctx, s.declarations.ReceiverID, assessments, s)
	if err != nil {
		return OwnedAdmissionReviewCatalog{}, OwnedAdmissionReviewReceipt{}, err
	}
	strategies, err := readPaperStrategies(ctx, tx, s.declarations.ReceiverID)
	if err != nil {
		return OwnedAdmissionReviewCatalog{}, OwnedAdmissionReviewReceipt{}, err
	}
	var raw []byte
	var state domain.State
	if err = tx.QueryRow(ctx, "SELECT payload FROM mantik_state WHERE id='demo'").Scan(&raw); err != nil {
		return OwnedAdmissionReviewCatalog{}, OwnedAdmissionReviewReceipt{}, err
	}
	if json.Unmarshal(raw, &state) != nil {
		return OwnedAdmissionReviewCatalog{}, OwnedAdmissionReviewReceipt{}, ErrOwnedAdmissionReviewInvalid
	}
	policies, err := readAllocationPolicies(ctx, tx, s.declarations.ReceiverID, state)
	if err != nil {
		return OwnedAdmissionReviewCatalog{}, OwnedAdmissionReviewReceipt{}, err
	}
	journal, err := readOwnedAllocationJournal(ctx, tx)
	if err != nil {
		return OwnedAdmissionReviewCatalog{}, OwnedAdmissionReviewReceipt{}, err
	}
	source, err := replayOwnedAllocations(ctx, s.declarations.ReceiverID, journal, policies, s.decisions, strategies, s.retained, s.declarations, actual)
	if err != nil {
		return OwnedAdmissionReviewCatalog{}, OwnedAdmissionReviewReceipt{}, err
	}
	all, err := readOwnedAdmissionReviews(ctx, tx)
	if err != nil {
		return OwnedAdmissionReviewCatalog{}, OwnedAdmissionReviewReceipt{}, err
	}
	var receipt OwnedAdmissionReviewReceipt
	if write {
		receipt, err = appendOwnedAdmissionReview(ctx, source.ReceiverID, all, source, c, now, s.sourceClock)
		if err != nil {
			return OwnedAdmissionReviewCatalog{}, OwnedAdmissionReviewReceipt{}, err
		}
		if !receipt.Duplicate {
			raw, err = json.Marshal(receipt.Record)
			if err != nil {
				return OwnedAdmissionReviewCatalog{}, OwnedAdmissionReviewReceipt{}, err
			}
			if _, err = tx.Exec(ctx, "INSERT INTO mantik_owned_admission_reviews(id,payload) VALUES($1,$2)", c.ID, raw); err != nil {
				return OwnedAdmissionReviewCatalog{}, OwnedAdmissionReviewReceipt{}, err
			}
			all[c.ID] = receipt.Record
		}
	}
	catalog, err := ownedAdmissionReviewCatalog(ctx, source.ReceiverID, all, source)
	if err != nil {
		return OwnedAdmissionReviewCatalog{}, OwnedAdmissionReviewReceipt{}, err
	}
	// Neither a staged row nor a successful INSERT acknowledges a durable review.
	if err = tx.Commit(ctx); err != nil {
		return OwnedAdmissionReviewCatalog{}, OwnedAdmissionReviewReceipt{}, err
	}
	return catalog, receipt, nil
}
func (p *Postgres) ReviewOwnedAdmission(ctx context.Context, c OwnedAdmissionReviewCommand, now func() time.Time, de PaperDecisionEvaluator, oe PaperOutcomeEvaluator) (OwnedAdmissionReviewReceipt, error) {
	if c.Validate() != nil {
		return OwnedAdmissionReviewReceipt{}, ErrOwnedAdmissionReviewInvalid
	}
	_, r, err := p.admissionReviewAccess(ctx, true, c, now, de, oe)
	return r, err
}
func (p *Postgres) OwnedAdmissionReviews(ctx context.Context, de PaperDecisionEvaluator, oe PaperOutcomeEvaluator) (OwnedAdmissionReviewCatalog, error) {
	c, _, err := p.admissionReviewAccess(ctx, false, OwnedAdmissionReviewCommand{}, nil, de, oe)
	return c, err
}

var _ OwnedAdmissionReviewStore = (*Memory)(nil)
var _ OwnedAdmissionReviewStore = (*Postgres)(nil)
