package storage

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"regexp"
	"slices"
	"strings"
	"time"

	"bytes"
	"git.subcult.tv/subculture-collective/mantik/internal/domain"
	"git.subcult.tv/subculture-collective/mantik/internal/feedcollector"
	"git.subcult.tv/subculture-collective/mantik/internal/paperaccount"
	"git.subcult.tv/subculture-collective/mantik/internal/paperstrategy"
	"git.subcult.tv/subculture-collective/mantik/internal/workerplan"
	"github.com/jackc/pgx/v5"
)

const MaxPaperDecisions = 32
const MaxPaperDecisionBytes = 2 << 20

var ErrPaperDecisionInvalid = errors.New("invalid owned paper decision")
var ErrPaperDecisionConflict = errors.New("owned paper decision identity or source conflict")
var ErrPaperDecisionCapacity = errors.New("owned paper decision capacity exhausted")
var ErrPaperDecisionNotFound = errors.New("owned paper decision not found")
var decisionPin = regexp.MustCompile(`^[a-f0-9]{64}$`)
var decisionRegistration = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,128}$`)

// Public callers select retained identities only. No evaluation, clock, account,
// admission, balance, quote or effect assertions are accepted.
type PaperDecisionCommand struct {
	ID             string `json:"id"`
	ReceiverID     string `json:"receiver_id"`
	StrategyID     string `json:"strategy_id"`
	StrategySHA256 string `json:"strategy_sha256"`
	RegistrationID string `json:"registration_id"`
}

func (c PaperDecisionCommand) Validate() error {
	if !paperaccount.ID.MatchString(c.ID) || !ValidReceiverID(c.ReceiverID) || !paperaccount.ID.MatchString(c.StrategyID) || !decisionPin.MatchString(c.StrategySHA256) || !decisionRegistration.MatchString(c.RegistrationID) {
		return ErrPaperDecisionInvalid
	}
	return nil
}

type PaperDecisionBasis struct {
	Strategy paperstrategy.Record `json:"strategy"`
	Feed     FeedEvidenceSnapshot `json:"feed"`
}
type PaperDecisionEvaluation struct {
	Action     string                      `json:"action"`
	ReasonCode string                      `json:"reason_code"`
	Coverage   domain.MarketCoverageReport `json:"coverage"`
	PriceCents int64                       `json:"price_cents"`
	Signal     bool                        `json:"signal"`
}
type PaperDecisionRecord struct {
	Schema              string                  `json:"schema"`
	Command             PaperDecisionCommand    `json:"command"`
	FinalizedAt         time.Time               `json:"finalized_at"`
	Status              string                  `json:"status"`
	Evaluation          PaperDecisionEvaluation `json:"evaluation"`
	BasisSHA256         string                  `json:"basis_sha256"`
	Authority           string                  `json:"authority"`
	AdmissionAvailable  bool                    `json:"admission_available"`
	ExecutionAvailable  bool                    `json:"execution_available"`
	QualificationCredit bool                    `json:"qualification_credit"`
	SHA256              string                  `json:"sha256"`
}

// Validate checks the public receipt envelope, not service ownership of its basis.
func (r PaperDecisionRecord) Validate() error {
	if r.Command.Validate() != nil || r.Schema != "mantik-owned-paper-decision-v1" || r.Status != "finalized" || r.FinalizedAt.IsZero() || r.Authority != "service_finalized_research_proposal_only" || r.AdmissionAvailable || r.ExecutionAvailable || r.QualificationCredit || !decisionPin.MatchString(r.BasisSHA256) || (r.Evaluation.Action != "abstain" && r.Evaluation.Action != "entry_candidate") || r.Evaluation.ReasonCode == "" {
		return ErrPaperDecisionInvalid
	}
	want := r.SHA256
	r.SHA256 = ""
	if !decisionPin.MatchString(want) || workerplan.Hash(r) != want {
		return ErrPaperDecisionInvalid
	}
	return nil
}

type PaperDecisionReceipt struct {
	Record    PaperDecisionRecord `json:"record"`
	Duplicate bool                `json:"duplicate"`
}
type PaperDecisionCatalog struct {
	ReceiverID string                `json:"receiver_id"`
	Records    []PaperDecisionRecord `json:"records"`
}
type retainedPaperDecision struct {
	Record PaperDecisionRecord `json:"record"`
	Basis  PaperDecisionBasis  `json:"basis"`
}

// The evaluator is service code, runs without I/O under the transaction locks,
// and cannot mutate retained inputs. It is never supplied over HTTP.
type PaperDecisionEvaluator func(PaperDecisionCommand, PaperDecisionBasis, time.Time) (PaperDecisionEvaluation, error)
type PaperDecisionStore interface {
	FinalizePaperDecision(context.Context, PaperDecisionCommand, func() time.Time, PaperDecisionEvaluator) (PaperDecisionReceipt, error)
	PaperDecisions(context.Context, PaperDecisionEvaluator) (PaperDecisionCatalog, error)
}

func decisionBasis(c PaperDecisionCommand, strategies paperstrategy.Catalog, feed FeedEvidenceSnapshot) (PaperDecisionBasis, error) {
	s, err := paperstrategy.Select(strategies, c.StrategyID)
	if err != nil {
		return PaperDecisionBasis{}, err
	}
	if s.SHA256 != c.StrategySHA256 || strategies.ReceiverID != c.ReceiverID || feed.ReceiverID != c.ReceiverID {
		return PaperDecisionBasis{}, ErrPaperDecisionConflict
	}
	e, ok := feed.Collectors.Entries[c.RegistrationID]
	if !ok {
		return PaperDecisionBasis{}, ErrPaperDecisionNotFound
	}
	selected := FeedEvidenceSnapshot{ReceiverID: feed.ReceiverID, Collectors: feedcollector.InitialState(), Routing: &domain.RoutingState{Inputs: map[string]domain.RetainedRoutingInput{}}, Records: map[string]FeedEvidenceRecord{}}
	selected.Collectors.LastClock = feed.Collectors.LastClock
	selected.Collectors.Entries[c.RegistrationID] = e
	for k, r := range feed.Records {
		if r.RegistrationID == c.RegistrationID {
			selected.Records[k] = r
		}
	}
	for id, a := range feed.Collectors.Admissions {
		if a.Fence.RegistrationID == c.RegistrationID {
			selected.Collectors.Admissions[id] = a
			if feed.Routing == nil {
				return PaperDecisionBasis{}, ErrPaperDecisionInvalid
			}
			v, ok := feed.Routing.Inputs[id]
			if !ok {
				return PaperDecisionBasis{}, ErrPaperDecisionInvalid
			}
			selected.Routing.Inputs[id] = v
		}
	}
	return clone(PaperDecisionBasis{Strategy: s, Feed: selected}), nil
}
func newPaperDecision(c PaperDecisionCommand, b PaperDecisionBasis, at time.Time, eval PaperDecisionEvaluator) (PaperDecisionRecord, error) {
	if c.Validate() != nil || eval == nil || at.IsZero() || at.UTC().Year() < 1 || at.UTC().Year() > 9999 || b.Strategy.Validate() != nil || b.Strategy.Command.ReceiverID != c.ReceiverID || b.Strategy.Command.Definition.ID != c.StrategyID || b.Strategy.SHA256 != c.StrategySHA256 || b.Feed.ReceiverID != c.ReceiverID || at.Before(b.Strategy.RegisteredAt) {
		return PaperDecisionRecord{}, ErrPaperDecisionInvalid
	}
	v, err := eval(c, clone(b), at.UTC())
	if err != nil {
		return PaperDecisionRecord{}, err
	}
	if (v.Action != "entry_candidate" && v.Action != "abstain") || v.ReasonCode == "" {
		return PaperDecisionRecord{}, ErrPaperDecisionInvalid
	}
	r := PaperDecisionRecord{Schema: "mantik-owned-paper-decision-v1", Command: c, FinalizedAt: at.UTC(), Status: "finalized", Evaluation: v, BasisSHA256: workerplan.Hash(b), Authority: "service_finalized_research_proposal_only"}
	r.SHA256 = workerplan.Hash(r)
	return r, nil
}
func sameFeedRecord(a, b FeedEvidenceRecord) bool {
	canonical := func(raw []byte) (string, error) {
		var value any
		d := json.NewDecoder(bytes.NewReader(raw))
		d.UseNumber()
		if err := d.Decode(&value); err != nil {
			return "", err
		}
		out, err := json.Marshal(value)
		return string(out), err
	}
	x, err := canonical(a.Recording)
	if err != nil {
		return false
	}
	y, err := canonical(b.Recording)
	if err != nil {
		return false
	}
	a.Recording = nil
	b.Recording = nil
	return reflect.DeepEqual(a, b) && x == y
}

// Validate historical sources against their actual retained owner records. A new
// lease or later input does not rewrite a completed decision's as-of basis.
func verifyPaperDecision(v retainedPaperDecision, strategies paperstrategy.Catalog, current FeedEvidenceSnapshot, eval PaperDecisionEvaluator) error {
	if err := verifyPaperDecisionBasis(v.Record.Command, v.Basis, v.Record.FinalizedAt, strategies, current); err != nil {
		return err
	}
	r, b, c := v.Record, v.Basis, v.Record.Command
	expected, err := newPaperDecision(c, b, r.FinalizedAt, eval)
	if err != nil || !reflect.DeepEqual(expected, r) {
		return ErrPaperDecisionInvalid
	}
	return nil
}

// verifyPaperDecisionBasis validates retained sources without manufacturing a
// finalized decision. Outcome assessments share this custody check.
func verifyPaperDecisionBasis(c PaperDecisionCommand, b PaperDecisionBasis, asof time.Time, strategies paperstrategy.Catalog, current FeedEvidenceSnapshot) error {
	s, err := paperstrategy.Select(strategies, c.StrategyID)
	if err != nil || !reflect.DeepEqual(s, b.Strategy) || current.ReceiverID != c.ReceiverID || b.Feed.ReceiverID != c.ReceiverID {
		return ErrPaperDecisionConflict
	}
	e, ok := current.Collectors.Entries[c.RegistrationID]
	old, had := b.Feed.Collectors.Entries[c.RegistrationID]
	if !ok || !had || len(b.Feed.Collectors.Entries) != 1 || e.Registration != old.Registration || !e.RegisteredAt.Equal(old.RegisteredAt) || b.Feed.Collectors.Commands == nil || len(b.Feed.Collectors.Commands) != 0 || b.Feed.Routing == nil || len(b.Feed.Routing.Inputs) != len(b.Feed.Collectors.Admissions) {
		return ErrPaperDecisionConflict
	}
	leaseFound := old.Lease == nil
	if old.Lease != nil {
		for _, cmd := range current.Collectors.Commands {
			if cmd.Command.RegistrationID == c.RegistrationID && cmd.Receipt.At.Compare(asof) <= 0 && reflect.DeepEqual(cmd.Receipt.Entry, old) {
				leaseFound = true
				break
			}
		}
	}
	if !leaseFound {
		return ErrPaperDecisionConflict
	}
	for k, x := range b.Feed.Records {
		y, ok := current.Records[k]
		if !ok || x.RegistrationID != c.RegistrationID || !sameFeedRecord(x, y) {
			return ErrPaperDecisionConflict
		}
	}
	for id, x := range b.Feed.Collectors.Admissions {
		y, ok := current.Collectors.Admissions[id]
		if !ok || x.Fence.RegistrationID != c.RegistrationID || !reflect.DeepEqual(x, y) || current.Routing == nil || !reflect.DeepEqual(b.Feed.Routing.Inputs[id], current.Routing.Inputs[id]) {
			return ErrPaperDecisionConflict
		}
	}
	return nil
}
func paperDecisionCatalog(owner string, all map[string]retainedPaperDecision, strategies paperstrategy.Catalog, feed FeedEvidenceSnapshot, eval PaperDecisionEvaluator) (PaperDecisionCatalog, error) {
	out := PaperDecisionCatalog{ReceiverID: owner, Records: []PaperDecisionRecord{}}
	if eval == nil || !ValidReceiverID(owner) || len(all) > MaxPaperDecisions || strategies.Validate() != nil {
		return out, ErrPaperDecisionInvalid
	}
	for id, v := range all {
		if id != v.Record.Command.ID || v.Record.Command.ReceiverID != owner {
			return out, ErrPaperDecisionInvalid
		}
		if err := verifyPaperDecision(v, strategies, feed, eval); err != nil {
			return out, err
		}
		out.Records = append(out.Records, v.Record)
	}
	slices.SortFunc(out.Records, func(a, b PaperDecisionRecord) int { return strings.Compare(a.Command.ID, b.Command.ID) })
	return clone(out), nil
}
func appendPaperDecision(c PaperDecisionCommand, all map[string]retainedPaperDecision, strategies paperstrategy.Catalog, feed FeedEvidenceSnapshot, now func() time.Time, eval PaperDecisionEvaluator) (retainedPaperDecision, bool, error) {
	if _, err := paperDecisionCatalog(feed.ReceiverID, all, strategies, feed, eval); err != nil {
		return retainedPaperDecision{}, false, err
	}
	if c.Validate() != nil {
		return retainedPaperDecision{}, false, ErrPaperDecisionInvalid
	}
	if c.ReceiverID != feed.ReceiverID {
		return retainedPaperDecision{}, false, ErrReceiverMismatch
	}
	if old, ok := all[c.ID]; ok {
		if old.Record.Command != c {
			return retainedPaperDecision{}, false, ErrPaperDecisionConflict
		}
		return clone(old), true, nil
	}
	if len(all) >= MaxPaperDecisions {
		return retainedPaperDecision{}, false, ErrPaperDecisionCapacity
	}
	if now == nil {
		return retainedPaperDecision{}, false, ErrPaperDecisionInvalid
	}
	b, err := decisionBasis(c, strategies, feed)
	if err != nil {
		return retainedPaperDecision{}, false, err
	}
	r, err := newPaperDecision(c, b, now(), eval)
	if err != nil {
		return retainedPaperDecision{}, false, err
	}
	v := retainedPaperDecision{r, b}
	raw, err := json.Marshal(v)
	if err != nil || len(raw) > MaxPaperDecisionBytes {
		return retainedPaperDecision{}, false, ErrPaperDecisionCapacity
	}
	return v, false, nil
}
func (m *Memory) decisionFeedLocked() FeedEvidenceSnapshot {
	return FeedEvidenceSnapshot{m.receiverID, clone(m.collectors), clone(m.state.Routing), clone(m.evidence)}
}
func (m *Memory) FinalizePaperDecision(ctx context.Context, c PaperDecisionCommand, now func() time.Time, eval PaperDecisionEvaluator) (PaperDecisionReceipt, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return PaperDecisionReceipt{}, err
	}
	s, err := m.paperStrategiesLocked()
	if err != nil {
		return PaperDecisionReceipt{}, err
	}
	v, dup, err := appendPaperDecision(c, m.paperDecisions, s, m.decisionFeedLocked(), now, eval)
	if err != nil {
		return PaperDecisionReceipt{}, err
	}
	if err := ctx.Err(); err != nil {
		return PaperDecisionReceipt{}, err
	}
	if !dup {
		m.paperDecisions[c.ID] = clone(v)
	}
	return PaperDecisionReceipt{clone(v.Record), dup}, nil
}
func (m *Memory) PaperDecisions(ctx context.Context, eval PaperDecisionEvaluator) (PaperDecisionCatalog, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return PaperDecisionCatalog{}, err
	}
	s, err := m.paperStrategiesLocked()
	if err != nil {
		return PaperDecisionCatalog{}, err
	}
	return paperDecisionCatalog(m.receiverID, m.paperDecisions, s, m.decisionFeedLocked(), eval)
}
func readPaperDecisions(ctx context.Context, tx pgx.Tx) (map[string]retainedPaperDecision, error) {
	rows, err := tx.Query(ctx, "SELECT id,payload FROM mantik_paper_decisions ORDER BY id LIMIT $1", MaxPaperDecisions+1)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	all := map[string]retainedPaperDecision{}
	for rows.Next() {
		var id string
		var raw []byte
		var v retainedPaperDecision
		if err := rows.Scan(&id, &raw); err != nil {
			return nil, err
		}
		if workerplan.ReadBoundedShape(raw, &v, MaxPaperDecisionBytes) != nil || id != v.Record.Command.ID {
			return nil, ErrPaperDecisionInvalid
		}
		all[id] = v
	}
	if len(all) > MaxPaperDecisions {
		return nil, ErrPaperDecisionCapacity
	}
	return all, rows.Err()
}
func (p *Postgres) FinalizePaperDecision(ctx context.Context, c PaperDecisionCommand, now func() time.Time, eval PaperDecisionEvaluator) (PaperDecisionReceipt, error) {
	if c.Validate() != nil {
		return PaperDecisionReceipt{}, ErrPaperDecisionInvalid
	}
	tx, err := p.pool.Begin(ctx)
	if err != nil {
		return PaperDecisionReceipt{}, err
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	// Acquire the global row-order subset collector -> state -> receiver.
	// Feed publication/submission locks collector -> state, while capital policy
	// and fixture binding lock state -> receiver. Receiver-first here can cycle
	// with an owner holding state while waiting for receiver.
	feed, err := readFeedEvidence(ctx, tx, true)
	if err != nil {
		return PaperDecisionReceipt{}, err
	}
	var owner string
	if err = tx.QueryRow(ctx, "SELECT id FROM mantik_research_receiver WHERE singleton FOR UPDATE").Scan(&owner); err != nil {
		return PaperDecisionReceipt{}, err
	}
	if owner != c.ReceiverID || owner != feed.ReceiverID {
		return PaperDecisionReceipt{}, ErrReceiverMismatch
	}
	s, err := readPaperStrategies(ctx, tx, owner)
	if err != nil {
		return PaperDecisionReceipt{}, err
	}
	all, err := readPaperDecisions(ctx, tx)
	if err != nil {
		return PaperDecisionReceipt{}, err
	}
	v, dup, err := appendPaperDecision(c, all, s, feed, now, eval)
	if err != nil {
		return PaperDecisionReceipt{}, err
	}
	if !dup {
		raw, err := json.Marshal(v)
		if err != nil {
			return PaperDecisionReceipt{}, err
		}
		if _, err = tx.Exec(ctx, "INSERT INTO mantik_paper_decisions(id,payload) VALUES($1,$2)", c.ID, raw); err != nil {
			return PaperDecisionReceipt{}, err
		}
	}
	return PaperDecisionReceipt{v.Record, dup}, tx.Commit(ctx)
}
func (p *Postgres) PaperDecisions(ctx context.Context, eval PaperDecisionEvaluator) (PaperDecisionCatalog, error) {
	tx, err := p.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return PaperDecisionCatalog{}, err
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	feed, err := readFeedEvidence(ctx, tx, false)
	if err != nil {
		return PaperDecisionCatalog{}, err
	}
	s, err := readPaperStrategies(ctx, tx, feed.ReceiverID)
	if err != nil {
		return PaperDecisionCatalog{}, err
	}
	all, err := readPaperDecisions(ctx, tx)
	if err != nil {
		return PaperDecisionCatalog{}, err
	}
	out, err := paperDecisionCatalog(feed.ReceiverID, all, s, feed, eval)
	if err != nil {
		return PaperDecisionCatalog{}, err
	}
	return out, tx.Commit(ctx)
}
