package storage

import (
	"context"

	"git.subcult.tv/subculture-collective/mantik/internal/paperstrategy"
	"github.com/jackc/pgx/v5"
)

// A current working set is complete against actual active owner intervals,
// independently of how many records an older feed checkpoint closed. Historical
// replay retains its own complete original epoch/checkpoint contract.
func (m *Memory) activePaperOwnerRowsLocked(ctx context.Context, outcomes bool) ([]paperSampleRecord, error) {
	head := int64(len(m.paperRecordArchives))
	if _, err := m.paperArchiveEpochLocked(head); err != nil {
		return nil, err
	}
	rows := []paperSampleRecord{}
	for key, e := range m.paperRecordEpochs {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if e.Kind != "decision" && (!outcomes || e.Kind != "outcome") {
			continue
		}
		retired := m.paperRecordRetiredEpochs[key]
		if retired != 0 && (retired <= e.CreatedEpoch || retired > head) {
			return nil, ErrPaperRecordArchiveConflict
		}
		if retired > 0 {
			continue
		}
		r := paperSampleRecord{e, m.paperRecordScopes[key], retired}
		if key != e.Kind+":"+e.ID || r.RetiredEpoch != 0 || !validPaperSampleRecord(r, head) {
			return nil, ErrPaperRecordArchiveConflict
		}
		rows = append(rows, r)
		if len(rows) > MaxPaperDecisions+MaxPaperOutcomes {
			return nil, ErrPaperRecordArchiveCapacity
		}
	}
	return rows, nil
}

const activePaperOwnerQuery = `SELECT kind,record_id,created_epoch,record_sha256,at_seconds,at_nanos,
strategy_id,strategy_sha256,decision_id,decision_sha256,decision_at_seconds,decision_at_nanos,coalesce(retired_epoch,0)
FROM mantik_paper_record_epochs
WHERE int8range(created_epoch,retired_epoch,'[)') @> $1::bigint AND kind=ANY($2::text[])
LIMIT $3`

func readActivePaperOwnerRows(ctx context.Context, tx pgx.Tx, owner string, outcomes bool) ([]paperSampleRecord, error) {
	var head int64
	if err := tx.QueryRow(ctx, "SELECT coalesce(max(sequence),0) FROM mantik_paper_record_archives").Scan(&head); err != nil {
		return nil, err
	}
	if _, err := readPaperArchiveEpoch(ctx, tx, owner, head); err != nil {
		return nil, err
	}
	kinds := []string{"decision"}
	if outcomes {
		kinds = append(kinds, "outcome")
	}
	result, err := tx.Query(ctx, activePaperOwnerQuery, head, kinds, MaxPaperDecisions+MaxPaperOutcomes+1)
	if err != nil {
		return nil, err
	}
	defer result.Close()
	rows := []paperSampleRecord{}
	for result.Next() {
		r, err := scanPaperSampleRecord(result)
		if err != nil {
			return nil, err
		}
		if r.RetiredEpoch != 0 || !validPaperSampleRecord(r, head) {
			return nil, ErrPaperRecordArchiveConflict
		}
		rows = append(rows, r)
		if len(rows) > MaxPaperDecisions+MaxPaperOutcomes {
			return nil, ErrPaperRecordArchiveCapacity
		}
	}
	return rows, result.Err()
}

func activePaperDecisionCatalog(ctx context.Context, owner string, all map[string]retainedPaperDecision, strategies paperstrategy.Catalog, feed FeedEvidenceSnapshot, eval PaperDecisionEvaluator, rows []paperSampleRecord) (PaperDecisionCatalog, error) {
	seen := map[string]bool{}
	for _, r := range rows {
		if r.Kind != "decision" {
			continue
		}
		d, ok := all[r.ID]
		if seen[r.ID] || !ok || r.RetiredEpoch != 0 || d.Record.Command.ReceiverID != owner || r.RecordSHA256 != d.Record.SHA256 || !r.At.Equal(d.Record.FinalizedAt) || r.Scope != paperScopeForDecision(d.Record) {
			return PaperDecisionCatalog{}, ErrPaperDecisionConflict
		}
		seen[r.ID] = true
	}
	if len(seen) != len(all) {
		return PaperDecisionCatalog{}, ErrPaperDecisionConflict
	}
	out, err := replayPaperDecisionRecords(owner, all, strategies, feed, eval)
	if err != nil {
		return PaperDecisionCatalog{}, err
	}
	if err := ctx.Err(); err != nil {
		return PaperDecisionCatalog{}, err
	}
	return out, nil
}

func activePaperOutcomeCatalog(ctx context.Context, owner string, all map[string]retainedPaperOutcome, decisions map[string]retainedPaperDecision, strategies paperstrategy.Catalog, feed FeedEvidenceSnapshot, de PaperDecisionEvaluator, oe PaperOutcomeEvaluator, rows []paperSampleRecord) (PaperOutcomeCatalog, error) {
	if _, err := activePaperDecisionCatalog(ctx, owner, decisions, strategies, feed, de, rows); err != nil {
		return PaperOutcomeCatalog{}, err
	}
	seen := map[string]bool{}
	for _, r := range rows {
		if r.Kind != "outcome" {
			continue
		}
		o, ok := all[r.ID]
		if seen[r.ID] || !ok || r.RetiredEpoch != 0 || o.Record.Command.ReceiverID != owner || r.RecordSHA256 != o.Record.SHA256 || !r.At.Equal(o.Record.EvaluatedAt) || r.Scope != paperScopeForDecision(o.Basis.Decision) {
			return PaperOutcomeCatalog{}, ErrPaperOutcomeConflict
		}
		seen[r.ID] = true
	}
	if len(seen) != len(all) {
		return PaperOutcomeCatalog{}, ErrPaperOutcomeConflict
	}
	out, err := replayPaperOutcomeRecords(owner, all, decisions, strategies, feed, oe)
	if err != nil {
		return PaperOutcomeCatalog{}, err
	}
	if err := ctx.Err(); err != nil {
		return PaperOutcomeCatalog{}, err
	}
	return out, nil
}

func (m *Memory) activePaperDecisionsLocked(ctx context.Context, strategies paperstrategy.Catalog, feed FeedEvidenceSnapshot, eval PaperDecisionEvaluator) (PaperDecisionCatalog, error) {
	rows, err := m.activePaperOwnerRowsLocked(ctx, false)
	if err != nil {
		return PaperDecisionCatalog{}, err
	}
	return activePaperDecisionCatalog(ctx, m.receiverID, m.paperDecisions, strategies, feed, eval, rows)
}

func readActivePaperDecisions(ctx context.Context, tx pgx.Tx, owner string, all map[string]retainedPaperDecision, strategies paperstrategy.Catalog, feed FeedEvidenceSnapshot, eval PaperDecisionEvaluator) (PaperDecisionCatalog, error) {
	rows, err := readActivePaperOwnerRows(ctx, tx, owner, false)
	if err != nil {
		return PaperDecisionCatalog{}, err
	}
	return activePaperDecisionCatalog(ctx, owner, all, strategies, feed, eval, rows)
}

func (m *Memory) activePaperOutcomesLocked(ctx context.Context, strategies paperstrategy.Catalog, feed FeedEvidenceSnapshot, de PaperDecisionEvaluator, oe PaperOutcomeEvaluator) (PaperOutcomeCatalog, error) {
	rows, err := m.activePaperOwnerRowsLocked(ctx, true)
	if err != nil {
		return PaperOutcomeCatalog{}, err
	}
	return activePaperOutcomeCatalog(ctx, m.receiverID, m.paperOutcomes, m.paperDecisions, strategies, feed, de, oe, rows)
}

func readActivePaperOutcomes(ctx context.Context, tx pgx.Tx, owner string, all map[string]retainedPaperOutcome, decisions map[string]retainedPaperDecision, strategies paperstrategy.Catalog, feed FeedEvidenceSnapshot, de PaperDecisionEvaluator, oe PaperOutcomeEvaluator) (PaperOutcomeCatalog, error) {
	rows, err := readActivePaperOwnerRows(ctx, tx, owner, true)
	if err != nil {
		return PaperOutcomeCatalog{}, err
	}
	return activePaperOutcomeCatalog(ctx, owner, all, decisions, strategies, feed, de, oe, rows)
}
