package storage

import (
	"context"
	"slices"

	"git.subcult.tv/subculture-collective/mantik/internal/paperstrategy"
	"github.com/jackc/pgx/v5"
)

// An outcome can need a decision from a previous metadata page. Reserving half
// the payload page leaves room for every actual decision dependency while
// retaining the resolver's single-page byte and record bounds.
const maxPaperNativePage = maxPaperSamplePage / 2

func paperSampleNativeOwners(ctx context.Context, owner string, wanted []paperSampleRecord,
	dependencies func([]string) ([]paperSampleRecord, error),
	payloads func([]paperSampleRecord) (paperArchiveOwners, error),
	sources func([]string) (FeedEvidenceSnapshot, paperstrategy.Catalog, error),
	de PaperDecisionEvaluator, oe PaperOutcomeEvaluator,
) (paperArchiveOwners, error) {
	if err := ctx.Err(); err != nil {
		return paperArchiveOwners{}, err
	}
	if !ValidReceiverID(owner) || len(wanted) > maxPaperNativePage || de == nil || oe == nil {
		return paperArchiveOwners{}, ErrPaperRecordArchiveInvalid
	}
	selected, needed := map[string]bool{}, map[string]bool{}
	for _, w := range wanted {
		if w.Kind == "decision" {
			selected[w.ID] = true
		}
	}
	for _, w := range wanted {
		if w.Kind == "outcome" && !selected[w.Scope.DecisionID] {
			needed[w.Scope.DecisionID] = true
		}
	}
	ids := make([]string, 0, len(needed))
	for id := range needed {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	var more []paperSampleRecord
	if len(ids) > 0 {
		var err error
		more, err = dependencies(ids)
		if err != nil {
			return paperArchiveOwners{}, err
		}
	}
	if len(more) != len(needed) {
		return paperArchiveOwners{}, ErrPaperRecordArchiveConflict
	}
	for _, d := range more {
		if d.Kind != "decision" || !needed[d.ID] {
			return paperArchiveOwners{}, ErrPaperRecordArchiveConflict
		}
		delete(needed, d.ID)
	}
	page := append(append([]paperSampleRecord{}, wanted...), more...)
	owners, err := payloads(page)
	if err != nil {
		return paperArchiveOwners{}, err
	}
	feed, strategies, err := sources(paperFeedCustodyKeys(owners.decisions, owners.outcomes))
	if err != nil {
		return paperArchiveOwners{}, err
	}
	if feed.ReceiverID != owner || strategies.ReceiverID != owner || strategies.Validate() != nil {
		return paperArchiveOwners{}, ErrPaperRecordArchiveConflict
	}
	if err := validateFeedCustody(feed); err != nil {
		return paperArchiveOwners{}, err
	}
	index := newFeedPrefixIndex(feed)
	for id, d := range owners.decisions {
		if err := ctx.Err(); err != nil {
			return paperArchiveOwners{}, err
		}
		if id != d.Record.Command.ID || d.Record.Command.ReceiverID != owner {
			return paperArchiveOwners{}, ErrPaperDecisionConflict
		}
		if err := verifyPaperDecision(d, strategies, feed, index, de); err != nil {
			return paperArchiveOwners{}, err
		}
	}
	replay := newPaperOutcomeReplay(owner, owners.outcomes, strategies, feed, oe)
	for id, o := range owners.outcomes {
		if err := ctx.Err(); err != nil {
			return paperArchiveOwners{}, err
		}
		d, ok := owners.decisions[o.Record.Command.DecisionID]
		if !ok {
			return paperArchiveOwners{}, ErrPaperOutcomeConflict
		}
		if _, err := replay(id, o, d); err != nil {
			return paperArchiveOwners{}, err
		}
	}
	if err := ctx.Err(); err != nil {
		return paperArchiveOwners{}, err
	}
	return owners, nil
}

// These readers run inside the metadata pager's existing owner lock or
// transaction. Caller-supplied metadata cannot replace actual payload or source
// custody. Selection completeness remains the pager's responsibility.
func (m *Memory) paperSampleNativeLocked(ctx context.Context, owner string, wanted []paperSampleRecord, de PaperDecisionEvaluator, oe PaperOutcomeEvaluator) (paperArchiveOwners, error) {
	return paperSampleNativeOwners(ctx, owner, wanted, func(ids []string) ([]paperSampleRecord, error) {
		more := make([]paperSampleRecord, 0, len(ids))
		for _, id := range ids {
			key := "decision:" + id
			e, ok := m.paperRecordEpochs[key]
			scope, scoped := m.paperRecordScopes[key]
			if !ok || !scoped {
				return nil, ErrPaperRecordArchiveConflict
			}
			more = append(more, paperSampleRecord{e, scope, m.paperRecordRetiredEpochs[key]})
		}
		return more, nil
	}, func(records []paperSampleRecord) (paperArchiveOwners, error) {
		return m.paperSamplePayloadsLocked(ctx, owner, records)
	}, func(keys []string) (FeedEvidenceSnapshot, paperstrategy.Catalog, error) {
		feed := m.decisionFeedLocked()
		var err error
		feed.custody, err = m.feedCustodyForOwnersLocked(feed.Retention, keys)
		if err != nil {
			return FeedEvidenceSnapshot{}, paperstrategy.Catalog{}, err
		}
		strategies, err := m.paperStrategiesLocked()
		return feed, strategies, err
	}, de, oe)
}

func readPaperSampleNative(ctx context.Context, tx pgx.Tx, owner string, wanted []paperSampleRecord, de PaperDecisionEvaluator, oe PaperOutcomeEvaluator) (paperArchiveOwners, error) {
	return paperSampleNativeOwners(ctx, owner, wanted, func(ids []string) ([]paperSampleRecord, error) {
		rows, err := tx.Query(ctx, `SELECT kind,record_id,created_epoch,record_sha256,at_seconds,at_nanos,
strategy_id,strategy_sha256,decision_id,decision_sha256,decision_at_seconds,decision_at_nanos,coalesce(retired_epoch,0)
FROM mantik_paper_record_epochs WHERE kind='decision' AND record_id=ANY($1::text[])`, ids)
		if err != nil {
			return nil, err
		}
		defer rows.Close()
		more := make([]paperSampleRecord, 0, len(ids))
		for rows.Next() {
			r, err := scanPaperSampleRecord(rows)
			if err != nil {
				return nil, err
			}
			more = append(more, r)
		}
		return more, rows.Err()
	}, func(records []paperSampleRecord) (paperArchiveOwners, error) {
		return readPaperSamplePayloads(ctx, tx, owner, records)
	}, func(keys []string) (FeedEvidenceSnapshot, paperstrategy.Catalog, error) {
		feed, err := readFeedEvidence(ctx, tx, false)
		if err != nil {
			return FeedEvidenceSnapshot{}, paperstrategy.Catalog{}, err
		}
		feed.custody, err = readFeedCustodyForOwners(ctx, tx, feed.Retention, keys)
		if err != nil {
			return FeedEvidenceSnapshot{}, paperstrategy.Catalog{}, err
		}
		strategies, err := readPaperStrategies(ctx, tx, owner)
		return feed, strategies, err
	}, de, oe)
}
