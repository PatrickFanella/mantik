package storage

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"git.subcult.tv/subculture-collective/mantik/internal/workerplan"
)

const PaperRetirementSchema = "mantik-paper-record-retirement-v1"

var ErrPaperRetirementInvalid = errors.New("invalid paper record retirement request")
var ErrPaperRetirementConflict = errors.New("paper record retirement owner or archive conflict")

// PaperRetirementRetained counts the active decisions a retirement kept, each
// under the first reason that applies, in field order.
type PaperRetirementRetained struct {
	// Unsealed: the current paper archive does not hold the decision.
	Unsealed int `json:"unsealed"`
	// AllocationOpen: a record of the working allocation journal names the
	// decision, so an entry book or a pending effect may still need it.
	AllocationOpen int `json:"allocation_open"`
	// NewestDecision: the latest decision of its strategy record and
	// registration. The decision worker continues from it.
	NewestDecision int `json:"newest_decision"`
	// OutcomeUnsealed: one of its outcomes is newer than the current archive.
	OutcomeUnsealed int `json:"outcome_unsealed"`
	// OutcomePending: an entry candidate without an observed or
	// price-unavailable outcome.
	OutcomePending int `json:"outcome_pending"`
	// SharedSignal: another retained decision evaluated the same quote for
	// the same strategy record and registration.
	SharedSignal int `json:"shared_signal"`
	// CustodyBound: eligible, but a paper custody checkpoint already binds
	// the current archive epoch. They wait for the next paper archive.
	CustodyBound int `json:"custody_bound"`
}

// PaperRetirementReceipt reports one retirement transaction. Epoch is the
// paper archive that seals every retired record; the records stay readable by
// their original identity from that archive and leave the active catalogues.
type PaperRetirementReceipt struct {
	Schema              string                  `json:"schema"`
	ReceiverID          string                  `json:"receiver_id"`
	Epoch               int64                   `json:"epoch"`
	ArchiveSHA256       string                  `json:"archive_sha256,omitempty"`
	Retired             []PaperArchiveRecordPin `json:"retired"`
	Decisions           int                     `json:"decisions"`
	Outcomes            int                     `json:"outcomes"`
	Reviews             int                     `json:"reviews"`
	Retained            PaperRetirementRetained `json:"retained"`
	Authority           string                  `json:"authority"`
	AdmissionAvailable  bool                    `json:"admission_available"`
	ExecutionAvailable  bool                    `json:"execution_available"`
	QualificationCredit bool                    `json:"qualification_credit"`
}

// PaperRetirementStore retires sealed paper decisions, their outcomes and
// their claim reviews from the active working set. The owner selects the records; a caller cannot name
// one, and the result is an empty receipt when nothing is eligible.
type PaperRetirementStore interface {
	RetirePaperRecords(context.Context, string, PaperDecisionEvaluator, PaperOutcomeEvaluator) (PaperRetirementReceipt, error)
}

func newPaperRetirementReceipt(owner string, head PaperRecordArchiveHeader) PaperRetirementReceipt {
	return PaperRetirementReceipt{Schema: PaperRetirementSchema, ReceiverID: owner, Epoch: head.Sequence, ArchiveSHA256: head.SHA256, Retired: []PaperArchiveRecordPin{}, Authority: "service_owned_paper_record_retirement_only"}
}

type paperRetirementSources struct {
	// archive is the verified current (head) paper archive.
	archive   PaperRecordArchive
	decisions map[string]retainedPaperDecision
	outcomes  map[string]retainedPaperOutcome
	reviews   map[string]DecisionReviewRecord
	// working is the unsealed allocation journal.
	working []retainedOwnedAllocation
	// custodyBound: a paper custody checkpoint binds the head archive epoch.
	// Its replay selects the owners active at that epoch, so retiring one at
	// the same epoch would change a checkpoint that already committed.
	custodyBound bool
}

// selectPaperRetirement chooses the records that can leave the active working
// set at the head archive epoch. A decision retires together with all of its
// outcomes, and only when nothing that still runs reads it from the active
// catalogue: the decision worker (newest decision and shared signals), the
// outcome worker (pending outcomes) and allocation (working journal).
//
// A sealed claim review leaves once its decision has left or leaves in the
// same transaction. The newest review stays: it is the clock floor for the
// next one. Source-bound reviews stay too, because a new review of the same
// claim group and decision is refused only while the first one is active.
func selectPaperRetirement(s paperRetirementSources) ([]PaperArchiveRecordPin, PaperRetirementRetained, error) {
	var retained PaperRetirementRetained
	sealed := map[string]ArchivedPaperRecord{}
	for _, r := range s.archive.Records {
		sealed[r.Kind+":"+r.ID] = r
	}
	// The sealed original must equal the active record byte for byte: the
	// archive is all that remains readable after the active row is removed.
	pin := func(kind, id string, value any) (*PaperArchiveRecordPin, error) {
		r, ok := sealed[kind+":"+id]
		if !ok {
			return nil, nil
		}
		raw, err := json.Marshal(value)
		if err != nil {
			return nil, err
		}
		if !bytes.Equal(raw, r.Payload) {
			return nil, ErrPaperRetirementConflict
		}
		return &r.PaperArchiveRecordPin, nil
	}
	allocated := map[string]bool{}
	for _, item := range s.working {
		ids, err := allocationFinancialDecisions(item)
		if err != nil {
			return nil, retained, err
		}
		for _, id := range ids {
			allocated[id] = true
		}
	}
	pair := func(d PaperDecisionRecord) string { return d.Command.StrategySHA256 + "/" + d.Command.RegistrationID }
	newest := map[string]time.Time{}
	for _, d := range s.decisions {
		if at, ok := newest[pair(d.Record)]; !ok || d.Record.FinalizedAt.After(at) {
			newest[pair(d.Record)] = d.Record.FinalizedAt
		}
	}
	outcomes := map[string][]retainedPaperOutcome{}
	for _, o := range s.outcomes {
		id := o.Record.Command.DecisionID
		outcomes[id] = append(outcomes[id], o)
	}
	const (
		eligible = iota
		unsealed
		allocationOpen
		newestDecision
		outcomeUnsealed
		outcomePending
	)
	reasons := map[string]int{}
	pins := map[string][]PaperArchiveRecordPin{}
	signals := map[string][]string{}
	for id, d := range s.decisions {
		p, err := pin("decision", id, d)
		if err != nil {
			return nil, retained, err
		}
		selected := []PaperArchiveRecordPin{}
		observed, pending := false, false
		for _, o := range outcomes[id] {
			op, err := pin("outcome", o.Record.Command.ID, o)
			if err != nil {
				return nil, retained, err
			}
			if op == nil {
				pending = true
				continue
			}
			selected = append(selected, *op)
			if status := o.Record.Evaluation.Status; status == "observed" || status == "price_unavailable" {
				observed = true
			}
		}
		reason := eligible
		switch {
		case p == nil:
			reason = unsealed
		case allocated[id]:
			reason = allocationOpen
		case d.Record.FinalizedAt.Equal(newest[pair(d.Record)]):
			reason = newestDecision
		case pending:
			reason = outcomeUnsealed
		case d.Record.Evaluation.Action == "entry_candidate" && !observed:
			reason = outcomePending
		}
		reasons[id] = reason
		if p != nil {
			pins[id] = append(selected, *p)
		}
		if quote := d.Record.Evaluation.Coverage.Quote.InputID; quote != "" {
			key := pair(d.Record) + "/" + quote
			signals[key] = append(signals[key], id)
		}
	}
	shared := map[string]bool{}
	for _, ids := range signals {
		if slices.ContainsFunc(ids, func(id string) bool { return reasons[id] != eligible }) {
			for _, id := range ids {
				shared[id] = true
			}
		}
	}
	out := []PaperArchiveRecordPin{}
	for id, reason := range reasons {
		switch {
		case reason == unsealed:
			retained.Unsealed++
		case reason == allocationOpen:
			retained.AllocationOpen++
		case reason == newestDecision:
			retained.NewestDecision++
		case reason == outcomeUnsealed:
			retained.OutcomeUnsealed++
		case reason == outcomePending:
			retained.OutcomePending++
		case shared[id]:
			retained.SharedSignal++
		case s.custodyBound:
			retained.CustodyBound++
		default:
			out = append(out, pins[id]...)
		}
	}
	if !s.custodyBound {
		leaving := map[string]bool{}
		for _, p := range out {
			leaving[p.Kind+":"+p.ID] = true
		}
		var newestReview time.Time
		for _, r := range s.reviews {
			if r.ReviewedAt.After(newestReview) {
				newestReview = r.ReviewedAt
			}
		}
		for id, r := range s.reviews {
			_, active := s.decisions[r.Command.DecisionID]
			if r.Source != nil || r.Command.Source != nil || r.ReviewedAt.Equal(newestReview) || active && !leaving["decision:"+r.Command.DecisionID] {
				continue
			}
			p, err := pin("review", id, r)
			if err != nil {
				return nil, retained, err
			}
			if p != nil {
				out = append(out, *p)
			}
		}
	}
	slices.SortFunc(out, func(a, b PaperArchiveRecordPin) int { return strings.Compare(a.Kind+":"+a.ID, b.Kind+":"+b.ID) })
	return out, retained, nil
}

func (r *PaperRetirementReceipt) retire(pins []PaperArchiveRecordPin, retained PaperRetirementRetained) {
	r.Retired, r.Retained = pins, retained
	for _, p := range pins {
		switch p.Kind {
		case "decision":
			r.Decisions++
		case "outcome":
			r.Outcomes++
		default:
			r.Reviews++
		}
	}
}

func (m *Memory) RetirePaperRecords(ctx context.Context, owner string, de PaperDecisionEvaluator, oe PaperOutcomeEvaluator) (PaperRetirementReceipt, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if !ValidReceiverID(owner) || de == nil || oe == nil {
		return PaperRetirementReceipt{}, ErrPaperRetirementInvalid
	}
	if owner != m.receiverID {
		return PaperRetirementReceipt{}, ErrReceiverMismatch
	}
	if err := ctx.Err(); err != nil {
		return PaperRetirementReceipt{}, err
	}
	head := int64(len(m.paperRecordArchives))
	if head == 0 {
		return newPaperRetirementReceipt(owner, PaperRecordArchiveHeader{}), nil
	}
	// Replays the complete head archive against its original sources.
	a, err := m.paperRecordArchiveLocked(ctx, head, de, oe)
	if err != nil {
		return PaperRetirementReceipt{}, err
	}
	s := paperRetirementSources{archive: a, decisions: m.paperDecisions, outcomes: m.paperOutcomes, reviews: m.decisionReviews, working: m.ownedAllocations.Records}
	if n := len(m.paperFeedCheckpoints); n > 0 {
		s.custodyBound = m.paperFeedCheckpoints[n-1].PaperArchiveSequence >= head
	}
	pins, retained, err := selectPaperRetirement(s)
	if err != nil {
		return PaperRetirementReceipt{}, err
	}
	for _, p := range pins {
		key := p.Kind + ":" + p.ID
		if m.paperRecordEpochs[key] != p.PaperRecordEpoch || m.paperRecordRetiredEpochs[key] != 0 || p.CreatedEpoch >= head {
			return PaperRetirementReceipt{}, ErrPaperRetirementConflict
		}
	}
	if err := ctx.Err(); err != nil {
		return PaperRetirementReceipt{}, err
	}
	// Selection can fail before these owner mutations.
	if m.paperRecordRetiredEpochs == nil {
		m.paperRecordRetiredEpochs = map[string]int64{}
	}
	for _, p := range pins {
		m.paperRecordRetiredEpochs[p.Kind+":"+p.ID] = head
		switch p.Kind {
		case "decision":
			delete(m.paperDecisions, p.ID)
		case "outcome":
			delete(m.paperOutcomes, p.ID)
		default:
			delete(m.decisionReviews, p.ID)
		}
	}
	receipt := newPaperRetirementReceipt(owner, a.PaperRecordArchiveHeader)
	receipt.retire(pins, retained)
	return receipt, nil
}

func (p *Postgres) RetirePaperRecords(ctx context.Context, owner string, de PaperDecisionEvaluator, oe PaperOutcomeEvaluator) (PaperRetirementReceipt, error) {
	if !ValidReceiverID(owner) || de == nil || oe == nil {
		return PaperRetirementReceipt{}, ErrPaperRetirementInvalid
	}
	tx, err := p.pool.Begin(ctx)
	if err != nil {
		return PaperRetirementReceipt{}, err
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	// The same collector -> state -> receiver order as the paper archive.
	// Decision, outcome, allocation and custody writers wait on these locks.
	sources, err := readPaperEvaluationContext(ctx, tx, true, p.portfolioEvaluator)
	if err != nil {
		return PaperRetirementReceipt{}, err
	}
	if sources.feed.ReceiverID != owner {
		return PaperRetirementReceipt{}, ErrReceiverMismatch
	}
	var head int64
	if err := tx.QueryRow(ctx, "SELECT coalesce(max(sequence),0) FROM mantik_paper_record_archives").Scan(&head); err != nil {
		return PaperRetirementReceipt{}, err
	}
	if head == 0 {
		if err := tx.Commit(ctx); err != nil {
			return PaperRetirementReceipt{}, err
		}
		return newPaperRetirementReceipt(owner, PaperRecordArchiveHeader{}), nil
	}
	a, err := readPaperRecordArchive(ctx, tx, head, de, oe, p.portfolioEvaluator)
	if err != nil {
		return PaperRetirementReceipt{}, err
	}
	journal, err := readOwnedAllocationJournal(ctx, tx)
	if err != nil {
		return PaperRetirementReceipt{}, err
	}
	reviews, err := readDecisionReviews(ctx, tx)
	if err != nil {
		return PaperRetirementReceipt{}, err
	}
	s := paperRetirementSources{archive: a, decisions: sources.retained, outcomes: sources.retainedOutcomes, reviews: reviews, working: journal.Records}
	var raw []byte
	err = tx.QueryRow(ctx, "SELECT header FROM mantik_paper_feed_checkpoints ORDER BY sequence DESC LIMIT 1").Scan(&raw)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return PaperRetirementReceipt{}, err
	}
	if err == nil {
		var h PaperFeedCheckpointHeader
		if workerplan.ReadBoundedShape(raw, &h, 8192) != nil {
			return PaperRetirementReceipt{}, ErrPaperRetirementConflict
		}
		s.custodyBound = h.PaperArchiveSequence >= head
	}
	pins, retained, err := selectPaperRetirement(s)
	if err != nil {
		return PaperRetirementReceipt{}, err
	}
	kinds, ids := make([]string, len(pins)), make([]string, len(pins))
	tables := map[string]string{"decision": "mantik_paper_decisions", "outcome": "mantik_paper_outcomes", "review": "mantik_decision_reviews"}
	removals := map[string][]string{}
	for i, pin := range pins {
		kinds[i], ids[i] = pin.Kind, pin.ID
		removals[tables[pin.Kind]] = append(removals[tables[pin.Kind]], pin.ID)
	}
	for table, removed := range removals {
		tag, err := tx.Exec(ctx, "DELETE FROM "+table+" WHERE id=ANY($1::text[])", removed)
		if err != nil {
			return PaperRetirementReceipt{}, err
		}
		if tag.RowsAffected() != int64(len(removed)) {
			return PaperRetirementReceipt{}, ErrPaperRetirementConflict
		}
	}
	// The schema guard rechecks each row: unretired, sealed by the head
	// archive with this creation pin, already removed from its working table,
	// and no custody checkpoint at the epoch.
	tag, err := tx.Exec(ctx, "UPDATE mantik_paper_record_epochs e SET retired_epoch=$1 FROM unnest($2::text[],$3::text[]) AS w(kind,id) WHERE e.kind=w.kind AND e.record_id=w.id AND e.retired_epoch IS NULL", head, kinds, ids)
	if err != nil {
		return PaperRetirementReceipt{}, err
	}
	if tag.RowsAffected() != int64(len(pins)) {
		return PaperRetirementReceipt{}, ErrPaperRetirementConflict
	}
	if err := tx.Commit(ctx); err != nil {
		return PaperRetirementReceipt{}, err
	}
	receipt := newPaperRetirementReceipt(owner, a.PaperRecordArchiveHeader)
	receipt.retire(pins, retained)
	return receipt, nil
}

var _ PaperRetirementStore = (*Memory)(nil)
var _ PaperRetirementStore = (*Postgres)(nil)
