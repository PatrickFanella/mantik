package storage

import (
	"context"
	"encoding/json"
	"errors"

	"git.subcult.tv/subculture-collective/mantik/internal/workerplan"
	"github.com/jackc/pgx/v5"
)

func paperSamplePayloadRequest(owner string, wanted []paperSampleRecord, head int64) (map[string]paperSampleRecord, error) {
	if !ValidReceiverID(owner) || len(wanted) > maxPaperSamplePage {
		return nil, ErrPaperRecordArchiveInvalid
	}
	byID := make(map[string]paperSampleRecord, len(wanted))
	for _, r := range wanted {
		key := r.Kind + ":" + r.ID
		if _, duplicate := byID[key]; duplicate || !validPaperSampleRecord(r, head) {
			return nil, ErrPaperRecordArchiveInvalid
		}
		byID[key] = r
	}
	return byID, nil
}

// Payload custody is separate from native evaluation. The caller must still
// replay decisions/outcomes against actual owner sources before assessing them.
// Return no partial owners when any original, scope or envelope check fails.
func paperSamplePayloadOwners(ctx context.Context, owner string, wanted map[string]paperSampleRecord, rows []paperCheckpointOwner, head int64, original func(int64) (PaperRecordArchive, error)) (paperArchiveOwners, error) {
	if err := ctx.Err(); err != nil {
		return paperArchiveOwners{}, err
	}
	if len(rows) != len(wanted) {
		return paperArchiveOwners{}, ErrPaperRecordArchiveConflict
	}
	bytes := 0
	missing := map[string]bool{}
	for _, r := range rows {
		bytes += len(r.raw)
		if r.raw == nil {
			missing[r.Kind+":"+r.ID] = true
		}
	}
	if bytes > MaxPaperRecordArchiveBytes {
		return paperArchiveOwners{}, ErrPaperRecordArchiveCapacity
	}
	owners, err := paperCheckpointOwners(ctx, rows, head, func(sequence int64) (PaperRecordArchive, error) {
		a, err := original(sequence)
		if err != nil {
			return PaperRecordArchive{}, err
		}
		for _, r := range a.Records {
			if key := r.Kind + ":" + r.ID; missing[key] && r.CreatedEpoch+1 == sequence {
				bytes += len(r.Payload)
				delete(missing, key)
			}
		}
		if bytes > MaxPaperRecordArchiveBytes {
			return PaperRecordArchive{}, ErrPaperRecordArchiveCapacity
		}
		return a, nil
	})
	if err != nil {
		if errors.Is(err, ErrFeedArchiveConflict) || errors.Is(err, ErrPaperRecordArchiveNotFound) {
			err = ErrPaperRecordArchiveConflict
		}
		return paperArchiveOwners{}, err
	}
	for key, w := range wanted {
		if err := ctx.Err(); err != nil {
			return paperArchiveOwners{}, err
		}
		if w.Kind == "decision" {
			d, ok := owners.decisions[w.ID]
			if !ok || d.Record.Command.ReceiverID != owner || workerplan.Hash(d.Basis) != d.Record.BasisSHA256 || paperScopeForDecision(d.Record) != w.Scope {
				return paperArchiveOwners{}, ErrPaperRecordArchiveConflict
			}
		} else {
			o, ok := owners.outcomes[w.ID]
			if !ok || o.Record.Command.ReceiverID != owner || o.Basis.Decision.Command.ReceiverID != owner || o.Basis.Decision.Validate() != nil || workerplan.Hash(o.Basis) != o.Record.BasisSHA256 ||
				o.Record.Command.DecisionID != o.Basis.Decision.Command.ID || o.Record.Command.DecisionSHA256 != o.Basis.Decision.SHA256 || paperScopeForDecision(o.Basis.Decision) != w.Scope {
				return paperArchiveOwners{}, ErrPaperRecordArchiveConflict
			}
		}
		if key != w.Kind+":"+w.ID {
			return paperArchiveOwners{}, ErrPaperRecordArchiveConflict
		}
	}
	return owners, nil
}

func (m *Memory) paperSamplePayloadsLocked(ctx context.Context, owner string, wanted []paperSampleRecord) (paperArchiveOwners, error) {
	if err := ctx.Err(); err != nil {
		return paperArchiveOwners{}, err
	}
	if owner != m.receiverID {
		return paperArchiveOwners{}, ErrReceiverMismatch
	}
	head := int64(len(m.paperRecordArchives))
	byID, err := paperSamplePayloadRequest(owner, wanted, head)
	if err != nil {
		return paperArchiveOwners{}, err
	}
	rows := make([]paperCheckpointOwner, 0, len(wanted))
	bytes := 0
	for key, w := range byID {
		e, ok := m.paperRecordEpochs[key]
		scope, scoped := m.paperRecordScopes[key]
		retired := m.paperRecordRetiredEpochs[key]
		if !ok || !scoped || e != w.PaperRecordEpoch || scope != w.Scope || retired != w.RetiredEpoch {
			return paperArchiveOwners{}, ErrPaperRecordArchiveConflict
		}
		var raw []byte
		if w.Kind == "decision" {
			if r, ok := m.paperDecisions[w.ID]; ok {
				raw, err = json.Marshal(r)
			}
		} else if r, ok := m.paperOutcomes[w.ID]; ok {
			raw, err = json.Marshal(r)
		}
		if err != nil {
			return paperArchiveOwners{}, err
		}
		bytes += len(raw)
		if bytes > MaxPaperRecordArchiveBytes {
			return paperArchiveOwners{}, ErrPaperRecordArchiveCapacity
		}
		rows = append(rows, paperCheckpointOwner{e, retired, raw})
	}
	return paperSamplePayloadOwners(ctx, owner, byID, rows, head, m.paperRecordArchivePayloadLocked)
}

const paperSamplePayloadQuery = `SELECT e.kind,e.record_id,e.created_epoch,e.record_sha256,e.at_seconds,e.at_nanos,
e.strategy_id,e.strategy_sha256,e.decision_id,e.decision_sha256,e.decision_at_seconds,e.decision_at_nanos,coalesce(e.retired_epoch,0),
CASE e.kind WHEN 'decision' THEN (SELECT payload FROM mantik_paper_decisions WHERE id=e.record_id)
            WHEN 'outcome' THEN (SELECT payload FROM mantik_paper_outcomes WHERE id=e.record_id) END
FROM unnest($1::text[],$2::text[]) AS w(kind,id)
JOIN mantik_paper_record_epochs e ON e.kind=w.kind AND e.record_id=w.id`

func readPaperSamplePayloads(ctx context.Context, tx pgx.Tx, owner string, wanted []paperSampleRecord) (paperArchiveOwners, error) {
	var actualOwner string
	var head int64
	if err := tx.QueryRow(ctx, "SELECT id,(SELECT coalesce(max(sequence),0) FROM mantik_paper_record_archives) FROM mantik_research_receiver WHERE singleton").Scan(&actualOwner, &head); err != nil {
		return paperArchiveOwners{}, err
	}
	if owner != actualOwner {
		return paperArchiveOwners{}, ErrReceiverMismatch
	}
	byID, err := paperSamplePayloadRequest(owner, wanted, head)
	if err != nil {
		return paperArchiveOwners{}, err
	}
	kinds, ids := []string{}, []string{}
	for _, w := range wanted {
		kinds, ids = append(kinds, w.Kind), append(ids, w.ID)
	}
	result, err := tx.Query(ctx, paperSamplePayloadQuery, kinds, ids)
	if err != nil {
		return paperArchiveOwners{}, err
	}
	defer result.Close()
	rows := make([]paperCheckpointOwner, 0, len(wanted))
	bytes := 0
	for result.Next() {
		var raw []byte
		r, err := scanPaperSampleRecord(result, &raw)
		if err != nil {
			return paperArchiveOwners{}, err
		}
		if w, ok := byID[r.Kind+":"+r.ID]; !ok || r != w {
			return paperArchiveOwners{}, ErrPaperRecordArchiveConflict
		}
		bytes += len(raw)
		if bytes > MaxPaperRecordArchiveBytes {
			return paperArchiveOwners{}, ErrPaperRecordArchiveCapacity
		}
		rows = append(rows, paperCheckpointOwner{r.PaperRecordEpoch, r.RetiredEpoch, raw})
	}
	if err := result.Err(); err != nil {
		return paperArchiveOwners{}, err
	}
	result.Close()
	return paperSamplePayloadOwners(ctx, owner, byID, rows, head, func(n int64) (PaperRecordArchive, error) {
		return readPaperRecordArchivePayload(ctx, tx, n)
	})
}
