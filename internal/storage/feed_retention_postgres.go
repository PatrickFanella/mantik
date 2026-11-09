package storage

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"

	"git.subcult.tv/subculture-collective/mantik/internal/workerplan"
	"github.com/jackc/pgx/v5"
)

func readFeedRetention(ctx context.Context, tx pgx.Tx) (*FeedRetention, error) {
	var sequence, checkpoint int64
	var sha string
	var raw []byte
	err := tx.QueryRow(ctx, "SELECT sequence,checkpoint_sequence,sha256,payload FROM mantik_feed_retention ORDER BY sequence DESC LIMIT 1").Scan(&sequence, &checkpoint, &sha, &raw)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var r FeedRetention
	if workerplan.ReadBoundedShape(raw, &r, MaxFeedRetentionBytes) != nil || r.Validate() != nil || r.Sequence != sequence || r.Checkpoint.Sequence != checkpoint || r.SHA256 != sha {
		return nil, ErrFeedArchiveConflict
	}
	return &r, nil
}

func readFeedCustody(ctx context.Context, tx pgx.Tx, r *FeedRetention) (*feedCustodyView, error) {
	if r == nil {
		return nil, nil
	}
	cp, err := readPaperFeedCheckpoint(ctx, tx, r.Checkpoint.Sequence)
	if err != nil {
		return nil, err
	}
	if cp.PaperFeedCheckpointHeader != r.Checkpoint || !reflect.DeepEqual(cp.Prefixes, r.Prefixes) {
		return nil, ErrFeedArchiveConflict
	}
	v := &feedCustodyView{checkpoint: cp, seals: map[string]PaperFeedSourcePin{}}
	rows, err := tx.Query(ctx, "SELECT kind,record_id,retention_sequence,record_sha256,basis_sha256,payload FROM mantik_paper_feed_source_seals WHERE retention_sequence<=$1 ORDER BY kind,record_id LIMIT $2", r.Sequence, MaxPaperDecisions+MaxPaperOutcomes+1)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var kind, id, record, basis string
		var sequence int64
		var raw []byte
		if err := rows.Scan(&kind, &id, &sequence, &record, &basis, &raw); err != nil {
			return nil, err
		}
		var pin PaperFeedSourcePin
		if workerplan.ReadBoundedShape(raw, &pin, 2048) != nil || pin.Kind != kind || pin.ID != id || pin.RecordSHA256 != record || pin.BasisSHA256 != basis || sequence < 1 || sequence > r.Sequence {
			return nil, ErrFeedArchiveConflict
		}
		v.seals[kind+":"+id] = pin
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(v.seals) > MaxPaperDecisions+MaxPaperOutcomes {
		return nil, ErrFeedArchiveCapacity
	}
	return v, nil
}

func writePaperFeedSourceSeal(ctx context.Context, tx pgx.Tx, retirement *FeedRetention, pin PaperFeedSourcePin) error {
	if retirement == nil {
		return nil
	}
	raw, err := json.Marshal(pin)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, "INSERT INTO mantik_paper_feed_source_seals(kind,record_id,retention_sequence,record_sha256,basis_sha256,payload) VALUES($1,$2,$3,$4,$5,$6)", pin.Kind, pin.ID, retirement.Sequence, pin.RecordSHA256, pin.BasisSHA256, raw)
	return err
}

func (m *Memory) feedCustodyLocked(r *FeedRetention) (*feedCustodyView, error) {
	if r == nil {
		return nil, nil
	}
	if r.Validate() != nil || r.Checkpoint.Sequence > int64(len(m.paperFeedCheckpoints)) {
		return nil, ErrFeedArchiveConflict
	}
	cp := m.paperFeedCheckpoints[r.Checkpoint.Sequence-1]
	if cp.Validate() != nil || cp.PaperFeedCheckpointHeader != r.Checkpoint || !reflect.DeepEqual(cp.Prefixes, r.Prefixes) {
		return nil, ErrFeedArchiveConflict
	}
	return &feedCustodyView{checkpoint: cp, seals: m.paperFeedSourceSeals}, nil
}
