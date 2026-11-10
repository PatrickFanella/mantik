package storage

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"slices"

	"git.subcult.tv/subculture-collective/mantik/internal/domain"
	"git.subcult.tv/subculture-collective/mantik/internal/paperaccount"
	"git.subcult.tv/subculture-collective/mantik/internal/paperstrategy"
	"git.subcult.tv/subculture-collective/mantik/internal/socialwatch"
	"github.com/jackc/pgx/v5"
)

var ErrPaperAttributionNotFound = errors.New("paper attribution decision not found")

// ErrPaperAttributionIndexUnavailable reports sealed allocation history with
// no original segment index (legacy history before its rebuild). Callers can
// read the complete catalogue instead.
var ErrPaperAttributionIndexUnavailable = errors.New("paper attribution needs the allocation history index")

// PaperAttributionDecisionStore reads one active decision's attribution row.
// It replays the working allocation journal and only the sealed segments the
// decision index selects for that decision, not the whole archive.
type PaperAttributionDecisionStore interface {
	PaperAttributionDecision(context.Context, string, PaperDecisionEvaluator, PaperOutcomeEvaluator) (PaperAttributionRow, error)
}

// decisionAllocationCatalog keeps the natively replayed originals that name
// one decision: its sealed decision-history records, then the working
// journal's. Comparisons and execution steps keep their committed order, the
// order the complete catalogue lists them in.
func decisionAllocationCatalog(receiver, decision string, sealed []OwnedAllocationHistoryRecord, working ownedAllocationJournal, active OwnedAllocationCatalog) (OwnedAllocationCatalog, error) {
	out := OwnedAllocationCatalog{ReceiverID: receiver, Records: []OwnedAllocationRecord{}, Executions: []OwnedExecutionRecord{}}
	previous := 0
	for _, r := range sealed {
		if r.Sequence <= previous {
			return OwnedAllocationCatalog{}, ErrOwnedAllocationConflict
		}
		previous = r.Sequence
		switch {
		case r.Kind == "comparison" && r.Comparison != nil && r.Execution == nil:
			out.Records = append(out.Records, clone(*r.Comparison))
		case r.Kind == "execution" && r.Execution != nil && r.Comparison == nil:
			out.Executions = append(out.Executions, clone(*r.Execution))
		default:
			return OwnedAllocationCatalog{}, ErrOwnedAllocationConflict
		}
	}
	for _, item := range working.Records {
		ids, err := allocationFinancialDecisions(item)
		if err != nil {
			return OwnedAllocationCatalog{}, err
		}
		if !slices.Contains(ids, decision) {
			continue
		}
		if item.Execution != nil {
			found := false
			for _, e := range active.Executions {
				if e.Command.ID == item.Execution.Command.ID {
					out.Executions = append(out.Executions, clone(e))
					found = true
					break
				}
			}
			if !found {
				return OwnedAllocationCatalog{}, ErrOwnedAllocationConflict
			}
			continue
		}
		c, err := allocationComparisonFromCatalog(item.Record.Command.ID, active)
		if err != nil {
			return OwnedAllocationCatalog{}, ErrOwnedAllocationConflict
		}
		out.Records = append(out.Records, c)
	}
	return out, nil
}

// decisionAdmissionReviews validates one decision's review chain against the
// comparisons it names. The complete catalogue also checks every other
// decision's chain and the global review clock; this read does not.
func decisionAdmissionReviews(ctx context.Context, d PaperDecisionRecord, all map[string]OwnedAdmissionReview, source OwnedAllocationCatalog) ([]OwnedAdmissionReview, error) {
	if len(all) > MaxOwnedAdmissionReviews {
		return nil, ErrOwnedAdmissionReviewInvalid
	}
	out := []OwnedAdmissionReview{}
	for id, r := range all {
		if id != r.Command.ID {
			return nil, ErrOwnedAdmissionReviewInvalid
		}
		if r.Command.DecisionID == d.Command.ID {
			out = append(out, clone(r))
		}
	}
	slices.SortFunc(out, func(a, b OwnedAdmissionReview) int { return a.Sequence - b.Sequence })
	var previous OwnedAdmissionReview
	for _, r := range out {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		want, err := newOwnedAdmissionReview(r.Command, source, r.Sequence, r.ReviewedAt)
		if err != nil || r.Command.ReceiverID != source.ReceiverID || !reflect.DeepEqual(want, r) || r.Command.PreviousID != previous.Command.ID || r.Command.PreviousSHA256 != previous.SHA256 || r.Sequence <= previous.Sequence || r.ReviewedAt.Before(previous.ReviewedAt) {
			return nil, ErrOwnedAdmissionReviewInvalid
		}
		if previous.Command.ID != "" && (previous.Command.DecisionSHA256 != r.Command.DecisionSHA256 || r.ComparisonAt.Before(previous.ComparisonAt)) {
			return nil, ErrOwnedAdmissionReviewInvalid
		}
		previous = r
	}
	return out, nil
}

func activePaperDecision(id string, decisions PaperDecisionCatalog) (PaperDecisionRecord, error) {
	for _, d := range decisions.Records {
		if d.Command.ID == id {
			return d, nil
		}
	}
	return PaperDecisionRecord{}, ErrPaperAttributionNotFound
}

// paperAttributionDecision joins the selected owners into the decision's row,
// with the same row builder and claim-review join as the complete catalogue.
func paperAttributionDecision(ctx context.Context, d PaperDecisionRecord, decisions PaperDecisionCatalog, strategies paperstrategy.Catalog, outcomes PaperOutcomeCatalog, scoped OwnedAllocationCatalog, admission map[string]OwnedAdmissionReview, claims map[string]DecisionReviewRecord, sources []socialwatch.Journal) (PaperAttributionRow, error) {
	if outcomes.ReceiverID != decisions.ReceiverID || scoped.ReceiverID != decisions.ReceiverID {
		return PaperAttributionRow{}, ErrPaperAttributionInvalid
	}
	reviews, err := decisionAdmissionReviews(ctx, d, admission, scoped)
	if err != nil {
		return PaperAttributionRow{}, err
	}
	row, err := paperAttributionRow(d, decisions.ReceiverID, strategies, outcomes, scoped, reviews)
	if err != nil {
		return PaperAttributionRow{}, err
	}
	c, err := withClaimReviewAttribution(ctx, PaperAttributionCatalog{ReceiverID: decisions.ReceiverID, Rows: []PaperAttributionRow{row}}, decisions, claims, sources)
	if err != nil {
		return PaperAttributionRow{}, err
	}
	if len(c.Rows) != 1 {
		return PaperAttributionRow{}, ErrPaperAttributionInvalid
	}
	if raw, err := json.Marshal(c.Rows[0]); err != nil || len(raw) > MaxPaperAttributionBytes {
		return PaperAttributionRow{}, ErrPaperAttributionInvalid
	}
	if err := ctx.Err(); err != nil {
		return PaperAttributionRow{}, err
	}
	return c.Rows[0], nil
}

// readDecisionHistory follows one decision's sealed history pages to their
// end at one checkpoint cut, in the caller's lock or snapshot.
func readDecisionHistory(ctx context.Context, receiver, decision string, page func(OwnedAllocationHistoryQuery) (OwnedAllocationHistoryPage, error)) ([]OwnedAllocationHistoryRecord, error) {
	q := OwnedAllocationHistoryQuery{ReceiverID: receiver, DecisionID: decision, Limit: MaxOwnedAllocationHistoryPage}
	out := []OwnedAllocationHistoryRecord{}
	for range MaxOwnedAllocationContributionPages {
		p, err := page(q)
		if err != nil {
			return nil, err
		}
		out = append(out, p.Records...)
		if p.NextAfter == "" {
			return out, nil
		}
		q.After = p.NextAfter
	}
	return nil, ErrOwnedAllocationCapacity
}

func (m *Memory) PaperAttributionDecision(ctx context.Context, id string, de PaperDecisionEvaluator, oe PaperOutcomeEvaluator) (PaperAttributionRow, error) {
	if !paperaccount.ID.MatchString(id) || de == nil {
		return PaperAttributionRow{}, ErrPaperAttributionInvalid
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return PaperAttributionRow{}, err
	}
	_, decisions, strategies, err := m.ownedAllocationSourcesLocked(ctx, de)
	if err != nil {
		return PaperAttributionRow{}, err
	}
	d, err := activePaperDecision(id, decisions)
	if err != nil {
		return PaperAttributionRow{}, err
	}
	outcomes, err := m.activePaperOutcomesLocked(ctx, strategies, m.decisionFeedLocked(), de, oe)
	if err != nil {
		return PaperAttributionRow{}, err
	}
	working := m.ownedAllocations
	if _, indexed := m.ownedAllocationIndex.Members[1]; working.Checkpoint != nil && !indexed {
		return PaperAttributionRow{}, ErrPaperAttributionIndexUnavailable
	}
	if err := allocationTargetDependencies(working); err != nil {
		return PaperAttributionRow{}, err
	}
	sealed, err := readDecisionHistory(ctx, m.receiverID, id, func(q OwnedAllocationHistoryQuery) (OwnedAllocationHistoryPage, error) {
		return m.allocationHistoryPageLocked(ctx, q, de, oe)
	})
	if err != nil {
		return PaperAttributionRow{}, err
	}
	active, err := m.replayAllocationReadLocked(ctx, working, nil, de, oe)
	if err != nil {
		return PaperAttributionRow{}, err
	}
	scoped, err := decisionAllocationCatalog(m.receiverID, id, sealed, working, active)
	if err != nil {
		return PaperAttributionRow{}, err
	}
	sources, err := m.decisionReviewSocialSourcesLocked(DecisionReviewCommand{}, m.decisionReviews)
	if err != nil {
		return PaperAttributionRow{}, err
	}
	return paperAttributionDecision(ctx, d, decisions, strategies, outcomes, scoped, m.admissionReviews, m.decisionReviews, sources)
}

func (p *Postgres) PaperAttributionDecision(ctx context.Context, id string, de PaperDecisionEvaluator, oe PaperOutcomeEvaluator) (PaperAttributionRow, error) {
	if !paperaccount.ID.MatchString(id) || de == nil {
		return PaperAttributionRow{}, ErrPaperAttributionInvalid
	}
	tx, err := p.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return PaperAttributionRow{}, err
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	feed, err := readFeedEvidence(ctx, tx, false)
	if err != nil {
		return PaperAttributionRow{}, err
	}
	strategies, err := readPaperStrategies(ctx, tx, feed.ReceiverID)
	if err != nil {
		return PaperAttributionRow{}, err
	}
	retained, err := readPaperDecisions(ctx, tx)
	if err != nil {
		return PaperAttributionRow{}, err
	}
	decisions, err := readActivePaperDecisions(ctx, tx, feed.ReceiverID, retained, strategies, feed, de)
	if err != nil {
		return PaperAttributionRow{}, err
	}
	d, err := activePaperDecision(id, decisions)
	if err != nil {
		return PaperAttributionRow{}, err
	}
	all, err := readPaperOutcomes(ctx, tx)
	if err != nil {
		return PaperAttributionRow{}, err
	}
	outcomes, err := readActivePaperOutcomes(ctx, tx, feed.ReceiverID, all, retained, strategies, feed, de, oe)
	if err != nil {
		return PaperAttributionRow{}, err
	}
	var raw []byte
	var state domain.State
	if err = tx.QueryRow(ctx, "SELECT payload FROM mantik_state WHERE id='demo'").Scan(&raw); err != nil {
		return PaperAttributionRow{}, err
	}
	if json.Unmarshal(raw, &state) != nil {
		return PaperAttributionRow{}, ErrPaperAttributionInvalid
	}
	policies, err := readAllocationPolicies(ctx, tx, feed.ReceiverID, state)
	if err != nil {
		return PaperAttributionRow{}, err
	}
	plans, err := readPaperEvaluationDeclarations(ctx, tx)
	if err != nil {
		return PaperAttributionRow{}, err
	}
	declarations, err := paperEvaluationDeclarationCatalog(feed.ReceiverID, plans, strategies, policies)
	if err != nil {
		return PaperAttributionRow{}, err
	}
	assessments, err := readPaperEvaluationAssessments(ctx, tx)
	if err != nil {
		return PaperAttributionRow{}, err
	}
	assessmentCatalog, err := readPaperAssessmentOwners(ctx, tx, feed.ReceiverID, assessments, de, oe, p.portfolioEvaluator)
	if err != nil {
		return PaperAttributionRow{}, err
	}
	// The working journal only: its checkpoint stands in for the sealed
	// prefix, and the decision index selects that prefix's relevant segments.
	working, err := readOwnedAllocationJournal(ctx, tx)
	if err != nil {
		return PaperAttributionRow{}, err
	}
	if working.Checkpoint != nil {
		var indexed bool
		if err := tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM mantik_owned_allocation_members WHERE sequence=1)").Scan(&indexed); err != nil {
			return PaperAttributionRow{}, err
		}
		if !indexed {
			return PaperAttributionRow{}, ErrPaperAttributionIndexUnavailable
		}
	}
	if err := allocationTargetDependencies(working); err != nil {
		return PaperAttributionRow{}, err
	}
	dependencies, owners, err := readAllocationOwnerDecisions(ctx, tx, decisions, retained, working, nil, de)
	if err != nil {
		return PaperAttributionRow{}, err
	}
	x := p.ownedExecution(feed)
	active, err := replayOwnedAllocations(ctx, feed.ReceiverID, working, policies, dependencies, strategies, owners, x, declarations, assessmentCatalog)
	if err != nil {
		return PaperAttributionRow{}, err
	}
	sealed, err := readDecisionHistory(ctx, feed.ReceiverID, id, func(q OwnedAllocationHistoryQuery) (OwnedAllocationHistoryPage, error) {
		selected, err := postgresAllocationHistorySelection(ctx, tx, q, working.Checkpoint)
		if err != nil {
			return OwnedAllocationHistoryPage{}, err
		}
		return finishAllocationHistory(ctx, q, selected, func(s ownedAllocationSegment) (OwnedAllocationCatalog, error) {
			segmentDecisions, segmentOwners, err := readAllocationOwnerDecisions(ctx, tx, dependencies, owners, ownedAllocationJournal{Checkpoint: s.Before, Records: s.Records}, nil, de)
			if err != nil {
				return OwnedAllocationCatalog{}, err
			}
			sources := paperEvaluationSources{policies: policies, decisions: segmentDecisions, strategies: strategies, retained: segmentOwners, declarations: declarations}
			return replayOwnedAllocationSegment(ctx, s, sources, x, assessmentCatalog)
		})
	})
	if err != nil {
		return PaperAttributionRow{}, err
	}
	scoped, err := decisionAllocationCatalog(feed.ReceiverID, id, sealed, working, active)
	if err != nil {
		return PaperAttributionRow{}, err
	}
	admission, err := readOwnedAdmissionReviews(ctx, tx)
	if err != nil {
		return PaperAttributionRow{}, err
	}
	claims, err := readDecisionReviews(ctx, tx)
	if err != nil {
		return PaperAttributionRow{}, err
	}
	var sources []socialwatch.Journal
	if decisionReviewNeedsSources(DecisionReviewCommand{}, claims) {
		j, err := readSocialJournal(ctx, tx, feed.ReceiverID)
		if err != nil {
			return PaperAttributionRow{}, err
		}
		sources = []socialwatch.Journal{j}
	}
	row, err := paperAttributionDecision(ctx, d, decisions, strategies, outcomes, scoped, admission, claims, sources)
	if err != nil {
		return PaperAttributionRow{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return PaperAttributionRow{}, err
	}
	return row, nil
}
