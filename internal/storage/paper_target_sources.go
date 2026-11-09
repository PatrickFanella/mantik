package storage

import (
	"context"
	"errors"

	"git.subcult.tv/subculture-collective/mantik/internal/paperstrategy"
	"github.com/jackc/pgx/v5"
)

func archivedPaperDecisionRetry(c PaperDecisionCommand, owners paperArchiveOwners) (PaperDecisionReceipt, error) {
	v, err := selectPaperDecisionSources(c.ID, owners.decisions)
	if err != nil {
		return PaperDecisionReceipt{}, err
	}
	if c != v.Decision.Command {
		return PaperDecisionReceipt{}, ErrPaperDecisionConflict
	}
	return PaperDecisionReceipt{Record: v.Decision, Duplicate: true}, nil
}

func archivedPaperOutcomeRetry(c PaperOutcomeCommand, owners paperArchiveOwners) (PaperOutcomeReceipt, error) {
	v, err := selectPaperOutcomeSources(c.ID, owners.outcomes)
	if err != nil {
		return PaperOutcomeReceipt{}, err
	}
	if c != v.Outcome.Command {
		return PaperOutcomeReceipt{}, ErrPaperOutcomeConflict
	}
	return PaperOutcomeReceipt{Record: v.Outcome, Duplicate: true}, nil
}

// Keep collector -> state -> receiver lock order without loading the current
// feed into a retry whose sources belong to an independently sealed original.
func lockPaperTargetOwner(ctx context.Context, tx pgx.Tx) (string, error) {
	var present bool
	for _, query := range []string{
		"SELECT true FROM mantik_feed_collectors WHERE singleton FOR UPDATE",
		"SELECT true FROM mantik_state WHERE id='demo' FOR UPDATE",
	} {
		if err := tx.QueryRow(ctx, query).Scan(&present); err != nil {
			return "", err
		}
	}
	var owner string
	err := tx.QueryRow(ctx, "SELECT id FROM mantik_research_receiver WHERE singleton FOR UPDATE").Scan(&owner)
	return owner, err
}

// Active source readers keep their complete-catalogue validation. Only an
// independently reserved, approved retired identity takes the original-owner
// path. Absence of an active row never authorizes archive fallback.
func (m *Memory) paperTargetMetadataLocked(ctx context.Context, kind, id string) (paperSampleRecord, bool, error) {
	if err := ctx.Err(); err != nil {
		return paperSampleRecord{}, false, err
	}
	key := kind + ":" + id
	e, exists := m.paperRecordEpochs[key]
	retired := m.paperRecordRetiredEpochs[key]
	if !exists {
		if retired != 0 || m.paperArchiveOriginals[key] != 0 {
			return paperSampleRecord{}, false, ErrPaperRecordArchiveConflict
		}
		return paperSampleRecord{}, false, nil
	}
	r := paperSampleRecord{e, m.paperRecordScopes[key], retired}
	if retired == 0 {
		return r, false, nil
	}
	if e.Kind != kind || e.ID != id || !validPaperSampleRecord(r, int64(len(m.paperRecordArchives))) {
		return paperSampleRecord{}, false, ErrPaperRecordArchiveConflict
	}
	return r, true, nil
}

func readPaperTargetMetadata(ctx context.Context, tx pgx.Tx, kind, id string) (paperSampleRecord, bool, error) {
	r, err := scanPaperSampleRecord(tx.QueryRow(ctx, `SELECT kind,record_id,created_epoch,record_sha256,at_seconds,at_nanos,
strategy_id,strategy_sha256,decision_id,decision_sha256,decision_at_seconds,decision_at_nanos,coalesce(retired_epoch,0)
FROM mantik_paper_record_epochs WHERE kind=$1 AND record_id=$2`, kind, id))
	if errors.Is(err, pgx.ErrNoRows) {
		var archived bool
		if err := tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM mantik_paper_archive_members WHERE kind=$1 AND record_id=$2)", kind, id).Scan(&archived); err != nil {
			return paperSampleRecord{}, false, err
		}
		if archived {
			return paperSampleRecord{}, false, ErrPaperRecordArchiveConflict
		}
		return paperSampleRecord{}, false, nil
	}
	if err != nil {
		return paperSampleRecord{}, false, err
	}
	if r.RetiredEpoch == 0 {
		return r, false, nil
	}
	var head int64
	if err := tx.QueryRow(ctx, "SELECT coalesce(max(sequence),0) FROM mantik_paper_record_archives").Scan(&head); err != nil {
		return paperSampleRecord{}, false, err
	}
	if r.Kind != kind || r.ID != id || !validPaperSampleRecord(r, head) {
		return paperSampleRecord{}, false, ErrPaperRecordArchiveConflict
	}
	return r, true, nil
}

func (m *Memory) archivedPaperTargetLocked(ctx context.Context, r paperSampleRecord, de PaperDecisionEvaluator, oe PaperOutcomeEvaluator) (paperArchiveOwners, error) {
	if r.RetiredEpoch == 0 {
		return paperArchiveOwners{}, ErrPaperRecordArchiveConflict
	}
	a, err := m.paperRecordArchivePayloadLocked(r.CreatedEpoch + 1)
	if err != nil {
		return paperArchiveOwners{}, err
	}
	return m.paperSampleNativeWithSourcesLocked(ctx, m.receiverID, []paperSampleRecord{r}, de, oe, func(keys []string) (FeedEvidenceSnapshot, paperstrategy.Catalog, error) {
		feed := a.Context.Feed
		view, err := m.feedCustodyForOwnersLocked(feed.Retention, keys)
		feed.custody = view
		return feed, a.Context.Strategies, err
	})
}

func readArchivedPaperTarget(ctx context.Context, tx pgx.Tx, r paperSampleRecord, de PaperDecisionEvaluator, oe PaperOutcomeEvaluator) (paperArchiveOwners, error) {
	if r.RetiredEpoch == 0 {
		return paperArchiveOwners{}, ErrPaperRecordArchiveConflict
	}
	a, err := readPaperRecordArchivePayload(ctx, tx, r.CreatedEpoch+1)
	if err != nil {
		return paperArchiveOwners{}, err
	}
	return readPaperSampleNativeWithSources(ctx, tx, a.ReceiverID, []paperSampleRecord{r}, de, oe, func(keys []string) (FeedEvidenceSnapshot, paperstrategy.Catalog, error) {
		feed := a.Context.Feed
		view, err := readFeedCustodyForOwners(ctx, tx, feed.Retention, keys)
		feed.custody = view
		return feed, a.Context.Strategies, err
	})
}
