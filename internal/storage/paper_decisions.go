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

const MaxPaperDecisions = 256
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
	// Prefix is set on v3 bases, which hold a window of the feed.
	Prefix *PaperDecisionFeedPrefix `json:"prefix,omitempty"`
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
	if r.Command.Validate() != nil || (r.Schema != "mantik-owned-paper-decision-v1" && r.Schema != "mantik-owned-paper-decision-v2" && r.Schema != paperDecisionSchemaV3 && r.Schema != paperDecisionSchemaV4) || r.Status != "finalized" || r.FinalizedAt.IsZero() || r.Authority != "service_finalized_research_proposal_only" || r.AdmissionAvailable || r.ExecutionAvailable || r.QualificationCredit || !decisionPin.MatchString(r.BasisSHA256) || (r.Evaluation.Action != "abstain" && r.Evaluation.Action != "entry_candidate") || r.Evaluation.ReasonCode == "" {
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
	return paperDecisionForSchema(paperDecisionSchemaV3, c, b, at, eval)
}

// Historical v1 records keep their exact envelopes and hashes. Only new v2
// decisions assert the selected-registration immutable source membership cut.
func paperDecisionForSchema(schema string, c PaperDecisionCommand, b PaperDecisionBasis, at time.Time, eval PaperDecisionEvaluator) (PaperDecisionRecord, error) {
	if schema != "mantik-owned-paper-decision-v1" && schema != "mantik-owned-paper-decision-v2" && schema != paperDecisionSchemaV3 && schema != paperDecisionSchemaV4 {
		return PaperDecisionRecord{}, ErrPaperDecisionInvalid
	}
	if schema == paperDecisionSchemaV4 && !canonicalPaperBasisValid(b) {
		return PaperDecisionRecord{}, ErrPaperDecisionInvalid
	}
	if c.Validate() != nil || eval == nil || at.IsZero() || at.UTC().Year() < 1 || at.UTC().Year() > 9999 || b.Strategy.Validate() != nil || b.Strategy.Command.ReceiverID != c.ReceiverID || b.Strategy.Command.Definition.ID != c.StrategyID || b.Strategy.SHA256 != c.StrategySHA256 || b.Feed.ReceiverID != c.ReceiverID || at.Before(b.Strategy.RegisteredAt) {
		return PaperDecisionRecord{}, ErrPaperDecisionInvalid
	}
	if err := paperDecisionSourceTimes(c, b, at); err != nil {
		return PaperDecisionRecord{}, err
	}
	v, err := eval(c, clone(b), at.UTC())
	if err != nil {
		return PaperDecisionRecord{}, err
	}
	if (v.Action != "entry_candidate" && v.Action != "abstain") || v.ReasonCode == "" {
		return PaperDecisionRecord{}, ErrPaperDecisionInvalid
	}
	r := PaperDecisionRecord{Schema: schema, Command: c, FinalizedAt: at.UTC(), Status: "finalized", Evaluation: v, BasisSHA256: workerplan.Hash(b), Authority: "service_finalized_research_proposal_only"}
	r.SHA256 = workerplan.Hash(r)
	return r, nil
}

// canonicalRecording normalizes JSON so a recording compares equal however
// its store reordered object keys.
func canonicalRecording(raw []byte) (string, error) {
	var value any
	d := json.NewDecoder(bytes.NewReader(raw))
	d.UseNumber()
	if err := d.Decode(&value); err != nil {
		return "", err
	}
	out, err := json.Marshal(value)
	return string(out), err
}

func sameFeedRecord(a, b FeedEvidenceRecord) bool {
	x, err := canonicalRecording(a.Recording)
	if err != nil {
		return false
	}
	y, err := canonicalRecording(b.Recording)
	if err != nil {
		return false
	}
	a.Recording = nil
	b.Recording = nil
	return reflect.DeepEqual(a, b) && x == y
}

// Validate historical sources against their actual retained owner records. A new
// lease or later input does not rewrite a completed decision's as-of basis.
func verifyPaperDecision(v retainedPaperDecision, strategies paperstrategy.Catalog, current FeedEvidenceSnapshot, index *feedPrefixIndex, eval PaperDecisionEvaluator) error {
	if current.Retention != nil {
		if err := verifyFeedSourcePin(current, decisionFeedSourcePin(v)); err != nil {
			return err
		}
		if err := verifyPaperDecisionHeaderBasis(v.Record.Command, v.Basis, v.Record.FinalizedAt, strategies, current); err != nil {
			return err
		}
		if err := verifyRetiredPaperBasis(v.Record.Command, v.Basis, v.Record.FinalizedAt, current); err != nil {
			return err
		}
		expected, err := paperDecisionForSchema(v.Record.Schema, v.Record.Command, v.Basis, v.Record.FinalizedAt, eval)
		if err != nil || !reflect.DeepEqual(expected, v.Record) {
			return ErrPaperDecisionInvalid
		}
		return nil
	}
	if err := verifyPaperDecisionBasis(v.Record.Command, v.Basis, v.Record.FinalizedAt, strategies, current); err != nil {
		return err
	}
	r, b, c := v.Record, v.Basis, v.Record.Command
	switch r.Schema {
	case "mantik-owned-paper-decision-v2":
		if err := paperDecisionSourceMembership(c, b, r.FinalizedAt, current); err != nil {
			return err
		}
	case paperDecisionSchemaV3, paperDecisionSchemaV4:
		if err := verifyFeedPrefix(c, b, r.FinalizedAt, time.Time{}, index); err != nil {
			return err
		}
	}
	expected, err := paperDecisionForSchema(r.Schema, c, b, r.FinalizedAt, eval)
	if err != nil || !reflect.DeepEqual(expected, r) {
		return ErrPaperDecisionInvalid
	}
	return nil
}

// Source writers cannot add a new custody event at or before a retained cut.
// Reconstruct immutable membership from actual owner availability, never from
// provider occurrence time. Lease chronology and legacy completeness are not
// established by this check.
func paperDecisionSourceMembership(c PaperDecisionCommand, b PaperDecisionBasis, at time.Time, current FeedEvidenceSnapshot) error {
	for id, admission := range current.Collectors.Admissions {
		if admission.Fence.RegistrationID == c.RegistrationID && !admission.At.After(at) {
			if _, ok := b.Feed.Collectors.Admissions[id]; !ok {
				return ErrPaperDecisionConflict
			}
		}
	}
	for id, publication := range current.Records {
		if publication.RegistrationID == c.RegistrationID && !publication.PublishedAt.After(at) {
			if _, ok := b.Feed.Records[id]; !ok {
				return ErrPaperDecisionConflict
			}
		}
	}
	return nil
}

// Service custody timestamps belong to the storage owner, independently of
// whether a compiled evaluator happens to inspect them. Membership in the
// current catalogue cannot make a later owner record available in an earlier
// decision basis. This does not attest a complete historical source prefix.
func paperDecisionSourceTimes(c PaperDecisionCommand, b PaperDecisionBasis, at time.Time) error {
	entry, ok := b.Feed.Collectors.Entries[c.RegistrationID]
	if at.IsZero() || at.UTC().Year() < 1 || at.UTC().Year() > 9999 || !ok || entry.RegisteredAt.IsZero() || entry.RegisteredAt.After(at) || b.Feed.Collectors.LastClock.IsZero() || b.Feed.Collectors.LastClock.After(at) {
		return ErrPaperDecisionInvalid
	}
	for _, admission := range b.Feed.Collectors.Admissions {
		if admission.At.IsZero() || admission.At.After(at) {
			return ErrPaperDecisionInvalid
		}
	}
	for _, publication := range b.Feed.Records {
		if publication.PublishedAt.IsZero() || publication.PublishedAt.After(at) {
			return ErrPaperDecisionInvalid
		}
	}
	return nil
}

// verifyPaperDecisionBasis validates retained sources without manufacturing a
// finalized decision. Outcome assessments share this custody check.
func verifyPaperDecisionHeaderBasis(c PaperDecisionCommand, b PaperDecisionBasis, asof time.Time, strategies paperstrategy.Catalog, current FeedEvidenceSnapshot) error {
	if err := paperDecisionSourceTimes(c, b, asof); err != nil {
		return err
	}
	s, err := paperstrategy.Select(strategies, c.StrategyID)
	if err != nil || !reflect.DeepEqual(s, b.Strategy) || current.ReceiverID != c.ReceiverID || b.Feed.ReceiverID != c.ReceiverID {
		return ErrPaperDecisionConflict
	}
	e, ok := current.Collectors.Entries[c.RegistrationID]
	old, had := b.Feed.Collectors.Entries[c.RegistrationID]
	if !ok || !had || len(b.Feed.Collectors.Entries) != 1 || e.Registration != old.Registration || !e.RegisteredAt.Equal(old.RegisteredAt) || b.Feed.Collectors.Commands == nil || len(b.Feed.Collectors.Commands) != 0 || b.Feed.Routing == nil || len(b.Feed.Routing.Inputs) != len(b.Feed.Collectors.Admissions) {
		return ErrPaperDecisionConflict
	}
	return nil
}
func verifyPaperDecisionBasis(c PaperDecisionCommand, b PaperDecisionBasis, asof time.Time, strategies paperstrategy.Catalog, current FeedEvidenceSnapshot) error {
	if err := verifyPaperDecisionHeaderBasis(c, b, asof, strategies, current); err != nil {
		return err
	}
	old := b.Feed.Collectors.Entries[c.RegistrationID]
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
	if err := validateFeedCustody(feed); err != nil {
		return out, err
	}
	if eval == nil || !ValidReceiverID(owner) || len(all) > MaxPaperDecisions || strategies.Validate() != nil {
		return out, ErrPaperDecisionInvalid
	}
	if feed.Retention != nil {
		closed := 0
		for _, v := range all {
			if !v.Record.FinalizedAt.After(feed.Retention.Checkpoint.CutAt) {
				closed++
			}
		}
		if closed != feed.Retention.Checkpoint.Decisions {
			return out, ErrPaperDecisionConflict
		}
	}
	index := newFeedPrefixIndex(feed)
	for id, v := range all {
		if id != v.Record.Command.ID || v.Record.Command.ReceiverID != owner {
			return out, ErrPaperDecisionInvalid
		}
		if err := verifyPaperDecision(v, strategies, feed, index, eval); err != nil {
			return out, err
		}
		out.Records = append(out.Records, v.Record)
	}
	slices.SortFunc(out.Records, func(a, b PaperDecisionRecord) int { return strings.Compare(a.Command.ID, b.Command.ID) })
	return clone(out), nil
}
func appendPaperDecision(c PaperDecisionCommand, all map[string]retainedPaperDecision, strategies paperstrategy.Catalog, feed FeedEvidenceSnapshot, now func() time.Time, eval PaperDecisionEvaluator) (retainedPaperDecision, bool, error) {
	return appendPaperDecisionFromFeed(c, all, strategies, feed, feed, now, eval)
}
func appendPaperDecisionFromFeed(c PaperDecisionCommand, all map[string]retainedPaperDecision, strategies paperstrategy.Catalog, feed, observation FeedEvidenceSnapshot, now func() time.Time, eval PaperDecisionEvaluator) (retainedPaperDecision, bool, error) {
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
	full, err := decisionBasis(c, strategies, observation)
	if err != nil {
		return retainedPaperDecision{}, false, err
	}
	at := now()
	b := withFeedPrefix(c, full, observation, at, time.Time{})
	var r PaperDecisionRecord
	if feed.Retention != nil {
		b, err = canonicalPaperBasis(b)
		if err != nil {
			return retainedPaperDecision{}, false, err
		}
		r, err = paperDecisionForSchema(paperDecisionSchemaV4, c, b, at, eval)
	} else {
		r, err = newPaperDecision(c, b, at, eval)
	}
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
	feed := FeedEvidenceSnapshot{ReceiverID: m.receiverID, Collectors: clone(m.collectors), Routing: clone(m.state.Routing), Records: clone(m.evidence), Retention: clone(m.feedRetention)}
	view, err := m.feedCustodyLocked(feed.Retention)
	if err != nil {
		feed.custody = &feedCustodyView{err: err}
	} else {
		feed.custody = view
	}
	return feed
}
func (m *Memory) FinalizePaperDecision(ctx context.Context, c PaperDecisionCommand, now func() time.Time, eval PaperDecisionEvaluator) (PaperDecisionReceipt, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return PaperDecisionReceipt{}, err
	}
	if c.Validate() != nil {
		return PaperDecisionReceipt{}, ErrPaperDecisionInvalid
	}
	if c.ReceiverID != m.receiverID {
		return PaperDecisionReceipt{}, ErrReceiverMismatch
	}
	metadata, retired, err := m.paperTargetMetadataLocked(ctx, "decision", c.ID)
	if err != nil {
		return PaperDecisionReceipt{}, err
	}
	if retired {
		owners, err := m.archivedPaperTargetLocked(ctx, metadata, eval, nil)
		if err != nil {
			return PaperDecisionReceipt{}, err
		}
		return archivedPaperDecisionRetry(c, owners)
	}
	if _, exists := m.paperDecisions[c.ID]; metadata.ID != "" && !exists {
		return PaperDecisionReceipt{}, ErrPaperDecisionConflict
	}
	s, err := m.paperStrategiesLocked()
	if err != nil {
		return PaperDecisionReceipt{}, err
	}
	feed := m.decisionFeedLocked()
	observation, ownedNow, err := decisionObservationFeed(c, m.paperDecisions, feed, now, func(reg string, since, at time.Time) (FeedEvidenceSnapshot, error) {
		return m.archivedOutcomeFeedLocked(ctx, feed, reg, since, at)
	})
	if err != nil {
		return PaperDecisionReceipt{}, err
	}
	v, dup, err := appendPaperDecisionFromFeed(c, m.paperDecisions, s, feed, observation, ownedNow, eval)
	if err != nil {
		return PaperDecisionReceipt{}, err
	}
	if err := ctx.Err(); err != nil {
		return PaperDecisionReceipt{}, err
	}
	if !dup {
		if err := m.paperFeedCheckpointClockLocked(v.Record.FinalizedAt); err != nil {
			return PaperDecisionReceipt{}, err
		}
		if err := paperEvaluationSourceClock(m.receiverID, m.evaluationAssessments, v.Record.FinalizedAt); err != nil {
			return PaperDecisionReceipt{}, err
		}
		m.retainPaperFeedSourceSeal(decisionFeedSourcePin(v))
		m.paperDecisions[c.ID] = clone(v)
		m.retainPaperRecordEpoch("decision", c.ID, v.Record.SHA256, v.Record.FinalizedAt)
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
	owner, err := lockPaperTargetOwner(ctx, tx)
	if err != nil {
		return PaperDecisionReceipt{}, err
	}
	if owner != c.ReceiverID {
		return PaperDecisionReceipt{}, ErrReceiverMismatch
	}
	metadata, retired, err := readPaperTargetMetadata(ctx, tx, "decision", c.ID)
	if err != nil {
		return PaperDecisionReceipt{}, err
	}
	if retired {
		owners, err := readArchivedPaperTarget(ctx, tx, metadata, eval, nil)
		if err != nil {
			return PaperDecisionReceipt{}, err
		}
		out, err := archivedPaperDecisionRetry(c, owners)
		if err != nil {
			return PaperDecisionReceipt{}, err
		}
		if err := tx.Commit(ctx); err != nil {
			return PaperDecisionReceipt{}, err
		}
		return out, nil
	}
	feed, err := readFeedEvidence(ctx, tx, false)
	if err != nil {
		return PaperDecisionReceipt{}, err
	}
	if owner != feed.ReceiverID {
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
	if _, exists := all[c.ID]; metadata.ID != "" && !exists {
		return PaperDecisionReceipt{}, ErrPaperDecisionConflict
	}
	observation, ownedNow, err := decisionObservationFeed(c, all, feed, now, func(reg string, since, at time.Time) (FeedEvidenceSnapshot, error) {
		return readArchivedOutcomeFeed(ctx, tx, feed, reg, since, at)
	})
	if err != nil {
		return PaperDecisionReceipt{}, err
	}
	v, dup, err := appendPaperDecisionFromFeed(c, all, s, feed, observation, ownedNow, eval)
	if err != nil {
		return PaperDecisionReceipt{}, err
	}
	if !dup {
		if err := readPaperFeedCheckpointClock(ctx, tx, owner, v.Record.FinalizedAt); err != nil {
			return PaperDecisionReceipt{}, err
		}
		assessments, e := readPaperEvaluationAssessments(ctx, tx)
		if e != nil {
			return PaperDecisionReceipt{}, e
		}
		if e = paperEvaluationSourceClock(owner, assessments, v.Record.FinalizedAt); e != nil {
			return PaperDecisionReceipt{}, e
		}
		raw, err := json.Marshal(v)
		if err != nil {
			return PaperDecisionReceipt{}, err
		}
		if _, err = tx.Exec(ctx, "INSERT INTO mantik_paper_decisions(id,payload) VALUES($1,$2)", c.ID, raw); err != nil {
			return PaperDecisionReceipt{}, err
		}
		if err := writePaperFeedSourceSeal(ctx, tx, feed.Retention, decisionFeedSourcePin(v)); err != nil {
			return PaperDecisionReceipt{}, err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return PaperDecisionReceipt{}, err
	}
	return PaperDecisionReceipt{v.Record, dup}, nil
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
	if err := tx.Commit(ctx); err != nil {
		return PaperDecisionCatalog{}, err
	}
	return out, nil
}
