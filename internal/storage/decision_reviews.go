package storage

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"git.subcult.tv/subculture-collective/mantik/internal/paperaccount"
	"git.subcult.tv/subculture-collective/mantik/internal/socialwatch"
	"git.subcult.tv/subculture-collective/mantik/internal/workerplan"
	"github.com/jackc/pgx/v5"
)

const MaxDecisionReviews = 64
const MaxDecisionReviewCommandBytes = 16384
const MaxDecisionReviewBytes = 32768

var ErrDecisionReviewInvalid = errors.New("invalid finalized decision review")
var ErrDecisionReviewConflict = errors.New("finalized decision review identity conflict")
var ErrDecisionReviewCapacity = errors.New("finalized decision review capacity exhausted")
var ErrDecisionReviewNotFound = errors.New("finalized decision review not found")
var reviewField = regexp.MustCompile(`^[a-z][a-z0-9_]{0,63}$`)

// Claims are caller assertions to check, never facts or financial instructions.
// Prose is retained verbatim and opaque: matching claims do not validate prose.
type DecisionReviewClaim struct {
	Field string `json:"field"`
	Value string `json:"value"`
}
type DecisionReviewSourceCommand struct {
	CaptureID     string `json:"capture_id"`
	CaptureSHA256 string `json:"capture_sha256"`
}

type DecisionReviewCommand struct {
	Source         *DecisionReviewSourceCommand `json:"source,omitempty"`
	ID             string                       `json:"id"`
	ReceiverID     string                       `json:"receiver_id"`
	DecisionID     string                       `json:"decision_id"`
	DecisionSHA256 string                       `json:"decision_sha256"`
	Explanation    string                       `json:"explanation,omitempty"`
	Claims         []DecisionReviewClaim        `json:"claims"`
}

func (c DecisionReviewCommand) Validate() error {
	if c.Source != nil && (!paperaccount.ID.MatchString(c.Source.CaptureID) || !decisionPin.MatchString(c.Source.CaptureSHA256) || c.Explanation != "") {
		return ErrDecisionReviewInvalid
	}
	if !paperaccount.ID.MatchString(c.ID) || !ValidReceiverID(c.ReceiverID) || !paperaccount.ID.MatchString(c.DecisionID) || !decisionPin.MatchString(c.DecisionSHA256) || len(c.Explanation) > 8192 || !utf8.ValidString(c.Explanation) || strings.ContainsRune(c.Explanation, 0) || len(c.Claims) < 1 || len(c.Claims) > 16 {
		return ErrDecisionReviewInvalid
	}
	for i, v := range c.Claims {
		if !reviewField.MatchString(v.Field) || len(v.Value) > 256 || !utf8.ValidString(v.Value) || strings.ContainsRune(v.Value, 0) || i > 0 && c.Claims[i-1].Field >= v.Field {
			return ErrDecisionReviewInvalid
		}
	}
	return nil
}

type DecisionReviewFinding struct {
	Claim    DecisionReviewClaim `json:"claim"`
	Status   string              `json:"status"`
	Expected *string             `json:"expected,omitempty"`
}
type DecisionReviewRecord struct {
	Source              *DecisionReviewSourceBinding `json:"source,omitempty"`
	Schema              string                       `json:"schema"`
	Method              string                       `json:"method"`
	Command             DecisionReviewCommand        `json:"command"`
	ReviewedAt          time.Time                    `json:"reviewed_at"`
	BasisSHA256         string                       `json:"basis_sha256"`
	Findings            []DecisionReviewFinding      `json:"findings"`
	Status              string                       `json:"status"`
	CaseID              string                       `json:"case_id,omitempty"`
	RecordPath          string                       `json:"record_path"`
	Authority           string                       `json:"authority"`
	ProseVerified       bool                         `json:"prose_verified"`
	ModelUsed           bool                         `json:"model_used"`
	AdmissionAvailable  bool                         `json:"admission_available"`
	ExecutionAvailable  bool                         `json:"execution_available"`
	QualificationCredit bool                         `json:"qualification_credit"`
	SHA256              string                       `json:"sha256"`
}
type DecisionReviewReceipt struct {
	Record    DecisionReviewRecord `json:"record"`
	Duplicate bool                 `json:"duplicate"`
}
type DecisionReviewCase struct {
	ID             string    `json:"id"`
	DecisionID     string    `json:"decision_id"`
	DecisionSHA256 string    `json:"decision_sha256"`
	BasisSHA256    string    `json:"basis_sha256"`
	OpenedAt       time.Time `json:"opened_at"`
	Status         string    `json:"status"`
	ReviewIDs      []string  `json:"review_ids"`
}
type DecisionReviewCatalog struct {
	ReceiverID string                 `json:"receiver_id"`
	Records    []DecisionReviewRecord `json:"records"`
	Cases      []DecisionReviewCase   `json:"cases"`
}
type DecisionReviewStore interface {
	ReviewPaperDecision(context.Context, DecisionReviewCommand, func() time.Time, PaperDecisionEvaluator) (DecisionReviewReceipt, error)
	DecisionReviews(context.Context, PaperDecisionEvaluator) (DecisionReviewCatalog, error)
}

func newDecisionReview(c DecisionReviewCommand, d PaperDecisionRecord, at time.Time) (DecisionReviewRecord, error) {
	if c.Validate() != nil || d.Validate() != nil || c.ReceiverID != d.Command.ReceiverID || c.DecisionID != d.Command.ID || c.DecisionSHA256 != d.SHA256 {
		return DecisionReviewRecord{}, ErrDecisionReviewConflict
	}
	if at.IsZero() || at.UTC().Year() < 1 || at.UTC().Year() > 9998 || at.Before(d.FinalizedAt) {
		return DecisionReviewRecord{}, ErrDecisionReviewInvalid
	}
	facts := map[string]string{"action": d.Evaluation.Action, "reason_code": d.Evaluation.ReasonCode, "reference_price_cents": strconv.FormatInt(d.Evaluation.PriceCents, 10), "stored_signal": strconv.FormatBool(d.Evaluation.Signal), "quote_input_id": d.Evaluation.Coverage.Quote.InputID, "basis_sha256": d.BasisSHA256, "strategy_id": d.Command.StrategyID, "strategy_sha256": d.Command.StrategySHA256, "finalized_at": d.FinalizedAt.UTC().Format(time.RFC3339Nano)}
	r := DecisionReviewRecord{Schema: "mantik-finalized-decision-review-v1", Method: "retained-decision-fact-claims-v1", Command: clone(c), ReviewedAt: at.UTC(), BasisSHA256: d.BasisSHA256, Findings: []DecisionReviewFinding{}, Status: "claims_consistent", RecordPath: "/v1/paper-decisions/" + d.Command.ID, Authority: "finalized_decision_claim_review_only"}
	for _, claim := range c.Claims {
		finding := DecisionReviewFinding{Claim: claim, Status: "unsupported_field"}
		if want, ok := facts[claim.Field]; ok {
			finding.Expected = &want
			finding.Status = "supported"
			if want != claim.Value {
				finding.Status = "contradiction"
			}
		}
		if finding.Status != "supported" {
			r.Status = "review_required"
			r.CaseID = "decision-review-" + d.SHA256
		}
		r.Findings = append(r.Findings, finding)
	}
	r.SHA256 = workerplan.Hash(r)
	raw, err := json.Marshal(r)
	if err != nil || len(raw) > MaxDecisionReviewBytes {
		return DecisionReviewRecord{}, ErrDecisionReviewCapacity
	}
	return r, nil
}
func decisionReviewCatalog(owner string, all map[string]DecisionReviewRecord, decisions PaperDecisionCatalog, sources ...socialwatch.Journal) (DecisionReviewCatalog, error) {
	out := DecisionReviewCatalog{ReceiverID: owner, Records: []DecisionReviewRecord{}, Cases: []DecisionReviewCase{}}
	if !ValidReceiverID(owner) || decisions.ReceiverID != owner || len(all) > MaxDecisionReviews {
		return out, ErrDecisionReviewInvalid
	}
	byID := map[string]PaperDecisionRecord{}
	for _, d := range decisions.Records {
		byID[d.Command.ID] = d
	}
	cases := map[string]DecisionReviewCase{}
	sourceGroups := map[string]bool{}
	for id, r := range all {
		d, ok := byID[r.Command.DecisionID]
		if !ok {
			return out, ErrDecisionReviewConflict
		}
		want, err := sourceDecisionReview(r.Command, d, r.ReviewedAt, sources)
		if err != nil || id != r.Command.ID || r.Command.ReceiverID != owner || !reflect.DeepEqual(want, r) {
			return out, ErrDecisionReviewInvalid
		}
		if r.Source != nil {
			key := r.Source.Kind + ":" + r.Source.ClaimGroupSHA256 + ":" + r.Command.DecisionSHA256
			if sourceGroups[key] {
				return out, ErrDecisionReviewConflict
			}
			sourceGroups[key] = true
		}
		out.Records = append(out.Records, clone(r))
		if r.CaseID != "" {
			c, exists := cases[r.CaseID]
			if !exists {
				c = DecisionReviewCase{ID: r.CaseID, DecisionID: d.Command.ID, DecisionSHA256: d.SHA256, BasisSHA256: d.BasisSHA256, OpenedAt: r.ReviewedAt, Status: "open", ReviewIDs: []string{}}
			}
			if r.ReviewedAt.Before(c.OpenedAt) {
				c.OpenedAt = r.ReviewedAt
			}
			c.ReviewIDs = append(c.ReviewIDs, id)
			cases[r.CaseID] = c
		}
	}
	for _, c := range cases {
		slices.Sort(c.ReviewIDs)
		out.Cases = append(out.Cases, c)
	}
	slices.SortFunc(out.Records, func(a, b DecisionReviewRecord) int { return strings.Compare(a.Command.ID, b.Command.ID) })
	slices.SortFunc(out.Cases, func(a, b DecisionReviewCase) int { return strings.Compare(a.ID, b.ID) })
	return out, nil
}
func appendDecisionReview(owner string, all map[string]DecisionReviewRecord, decisions PaperDecisionCatalog, c DecisionReviewCommand, now func() time.Time, sources ...socialwatch.Journal) (DecisionReviewReceipt, error) {
	catalog, err := decisionReviewCatalog(owner, all, decisions, sources...)
	if err != nil {
		return DecisionReviewReceipt{}, err
	}
	if c.Validate() != nil {
		return DecisionReviewReceipt{}, ErrDecisionReviewInvalid
	}
	if c.ReceiverID != owner {
		return DecisionReviewReceipt{}, ErrReceiverMismatch
	}
	if old, ok := all[c.ID]; ok {
		if !reflect.DeepEqual(c, old.Command) {
			return DecisionReviewReceipt{}, ErrDecisionReviewConflict
		}
		return DecisionReviewReceipt{clone(old), true}, nil
	}
	if len(all) >= MaxDecisionReviews {
		return DecisionReviewReceipt{}, ErrDecisionReviewCapacity
	}
	if now == nil {
		return DecisionReviewReceipt{}, ErrDecisionReviewInvalid
	}
	var selected *PaperDecisionRecord
	for _, d := range decisions.Records {
		if d.Command.ID == c.DecisionID {
			selected = &d
			break
		}
	}
	if selected == nil {
		return DecisionReviewReceipt{}, ErrDecisionReviewNotFound
	}
	at := now().UTC()
	for _, r := range catalog.Records {
		if at.Before(r.ReviewedAt) {
			return DecisionReviewReceipt{}, ErrDecisionReviewInvalid
		}
	}
	if c.Source != nil && len(sources) == 1 && len(sources[0].Entries) > 0 && sources[0].Entries[len(sources[0].Entries)-1].At.After(at) {
		return DecisionReviewReceipt{}, ErrDecisionReviewInvalid
	}
	r, err := sourceDecisionReview(c, *selected, at, sources)
	if err != nil {
		return DecisionReviewReceipt{}, err
	}
	if r.Source != nil {
		for _, old := range catalog.Records {
			if old.Source != nil && old.Source.Kind == r.Source.Kind && old.Source.ClaimGroupSHA256 == r.Source.ClaimGroupSHA256 && old.Command.DecisionSHA256 == c.DecisionSHA256 {
				return DecisionReviewReceipt{}, ErrDecisionReviewConflict
			}
		}
	}
	return DecisionReviewReceipt{r, false}, nil
}
func (m *Memory) ReviewPaperDecision(ctx context.Context, c DecisionReviewCommand, now func() time.Time, eval PaperDecisionEvaluator) (DecisionReviewReceipt, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return DecisionReviewReceipt{}, err
	}
	s, err := m.paperStrategiesLocked()
	if err != nil {
		return DecisionReviewReceipt{}, err
	}
	decisions, err := paperDecisionCatalog(m.receiverID, m.paperDecisions, s, m.decisionFeedLocked(), eval)
	if err != nil {
		return DecisionReviewReceipt{}, err
	}
	sources, err := m.decisionReviewSocialSourcesLocked(c, m.decisionReviews)
	if err != nil {
		return DecisionReviewReceipt{}, err
	}
	r, err := appendDecisionReview(m.receiverID, m.decisionReviews, decisions, c, now, sources...)
	if err != nil {
		return DecisionReviewReceipt{}, err
	}
	if err = ctx.Err(); err != nil {
		return DecisionReviewReceipt{}, err
	}
	if !r.Duplicate {
		m.decisionReviews[c.ID] = clone(r.Record)
		m.retainPaperRecordEpoch("review", c.ID, r.Record.SHA256, r.Record.ReviewedAt)
	}
	return r, nil
}
func (m *Memory) DecisionReviews(ctx context.Context, eval PaperDecisionEvaluator) (DecisionReviewCatalog, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return DecisionReviewCatalog{}, err
	}
	s, err := m.paperStrategiesLocked()
	if err != nil {
		return DecisionReviewCatalog{}, err
	}
	d, err := paperDecisionCatalog(m.receiverID, m.paperDecisions, s, m.decisionFeedLocked(), eval)
	if err != nil {
		return DecisionReviewCatalog{}, err
	}
	sources, err := m.decisionReviewSocialSourcesLocked(DecisionReviewCommand{}, m.decisionReviews)
	if err != nil {
		return DecisionReviewCatalog{}, err
	}
	result, err := decisionReviewCatalog(m.receiverID, m.decisionReviews, d, sources...)
	if err != nil {
		return DecisionReviewCatalog{}, err
	}
	return result, nil
}
func readDecisionReviews(ctx context.Context, tx pgx.Tx) (map[string]DecisionReviewRecord, error) {
	rows, err := tx.Query(ctx, "SELECT id,payload FROM mantik_decision_reviews ORDER BY id LIMIT $1", MaxDecisionReviews+1)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]DecisionReviewRecord{}
	for rows.Next() {
		var id string
		var raw []byte
		var r DecisionReviewRecord
		if err = rows.Scan(&id, &raw); err != nil {
			return nil, err
		}
		if workerplan.ReadBoundedShape(raw, &r, MaxDecisionReviewBytes) != nil || id != r.Command.ID {
			return nil, ErrDecisionReviewInvalid
		}
		out[id] = r
	}
	if len(out) > MaxDecisionReviews {
		return nil, ErrDecisionReviewCapacity
	}
	return out, rows.Err()
}
func (p *Postgres) decisionReviewAccess(ctx context.Context, write bool, c DecisionReviewCommand, now func() time.Time, eval PaperDecisionEvaluator) (DecisionReviewCatalog, DecisionReviewReceipt, error) {
	opts := pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly}
	if write {
		opts = pgx.TxOptions{}
	}
	tx, err := p.pool.BeginTx(ctx, opts)
	if err != nil {
		return DecisionReviewCatalog{}, DecisionReviewReceipt{}, err
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	// Same collector -> state -> receiver order as decision finalization.
	feed, err := readFeedEvidence(ctx, tx, write)
	if err != nil {
		return DecisionReviewCatalog{}, DecisionReviewReceipt{}, err
	}
	query := "SELECT id FROM mantik_research_receiver WHERE singleton"
	if write {
		query += " FOR UPDATE"
	}
	var owner string
	if err = tx.QueryRow(ctx, query).Scan(&owner); err != nil {
		return DecisionReviewCatalog{}, DecisionReviewReceipt{}, err
	}
	if owner != feed.ReceiverID {
		return DecisionReviewCatalog{}, DecisionReviewReceipt{}, ErrReceiverMismatch
	}
	s, err := readPaperStrategies(ctx, tx, owner)
	if err != nil {
		return DecisionReviewCatalog{}, DecisionReviewReceipt{}, err
	}
	retained, err := readPaperDecisions(ctx, tx)
	if err != nil {
		return DecisionReviewCatalog{}, DecisionReviewReceipt{}, err
	}
	decisions, err := paperDecisionCatalog(owner, retained, s, feed, eval)
	if err != nil {
		return DecisionReviewCatalog{}, DecisionReviewReceipt{}, err
	}
	all, err := readDecisionReviews(ctx, tx)
	if err != nil {
		return DecisionReviewCatalog{}, DecisionReviewReceipt{}, err
	}
	var sources []socialwatch.Journal
	if decisionReviewNeedsSources(c, all) {
		j, e := readSocialJournal(ctx, tx, owner)
		if e != nil {
			return DecisionReviewCatalog{}, DecisionReviewReceipt{}, e
		}
		sources = []socialwatch.Journal{j}
	}
	var receipt DecisionReviewReceipt
	if write {
		receipt, err = appendDecisionReview(owner, all, decisions, c, now, sources...)
		if err != nil {
			return DecisionReviewCatalog{}, DecisionReviewReceipt{}, err
		}
		if !receipt.Duplicate {
			raw, e := json.Marshal(receipt.Record)
			if e != nil {
				return DecisionReviewCatalog{}, DecisionReviewReceipt{}, e
			}
			if _, err = tx.Exec(ctx, "INSERT INTO mantik_decision_reviews(id,payload) VALUES($1,$2)", c.ID, raw); err != nil {
				return DecisionReviewCatalog{}, DecisionReviewReceipt{}, err
			}
			all[c.ID] = receipt.Record
		}
	}
	result, err := decisionReviewCatalog(owner, all, decisions, sources...)
	if err != nil {
		return DecisionReviewCatalog{}, DecisionReviewReceipt{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return DecisionReviewCatalog{}, DecisionReviewReceipt{}, err
	}
	return result, receipt, nil
}
func (p *Postgres) ReviewPaperDecision(ctx context.Context, c DecisionReviewCommand, now func() time.Time, eval PaperDecisionEvaluator) (DecisionReviewReceipt, error) {
	if c.Validate() != nil {
		return DecisionReviewReceipt{}, ErrDecisionReviewInvalid
	}
	_, r, e := p.decisionReviewAccess(ctx, true, c, now, eval)
	return r, e
}
func (p *Postgres) DecisionReviews(ctx context.Context, eval PaperDecisionEvaluator) (DecisionReviewCatalog, error) {
	c, _, e := p.decisionReviewAccess(ctx, false, DecisionReviewCommand{}, nil, eval)
	return c, e
}
