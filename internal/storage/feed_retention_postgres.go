package storage

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"

	"git.subcult.tv/subculture-collective/mantik/internal/paperaccount"
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

func paperFeedCustodyKeys(decisions map[string]retainedPaperDecision, outcomes map[string]retainedPaperOutcome) []string {
	return (paperArchiveOwners{decisions: decisions, outcomes: outcomes}).keys()
}

func readFeedCustody(ctx context.Context, tx pgx.Tx, r *FeedRetention) (*feedCustodyView, error) {
	if r == nil {
		return nil, nil
	}
	rows, err := tx.Query(ctx, "SELECT kind,id FROM (SELECT 'decision' AS kind,id FROM mantik_paper_decisions UNION ALL SELECT 'outcome',id FROM mantik_paper_outcomes) owners LIMIT $1", MaxPaperDecisions+MaxPaperOutcomes+1)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	keys := []string{}
	for rows.Next() {
		var kind, id string
		if err := rows.Scan(&kind, &id); err != nil {
			return nil, err
		}
		keys = append(keys, kind+":"+id)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	rows.Close()
	return readFeedCustodyForOwners(ctx, tx, r, keys)
}

// The scope is derived by an owner reader from actual active identities or a
// closed historical owner interval. It never comes from a request manifest.
const paperFeedCustodySealQuery = "SELECT s.kind,s.record_id,s.retention_sequence,s.record_sha256,s.basis_sha256,s.payload FROM unnest($2::text[],$3::text[]) AS owner(kind,id) JOIN LATERAL (SELECT * FROM mantik_paper_feed_source_seals WHERE kind=owner.kind AND record_id=owner.id AND retention_sequence<=$1 LIMIT 1) s ON true"

func readFeedCustodyForOwners(ctx context.Context, tx pgx.Tx, r *FeedRetention, keys []string) (*feedCustodyView, error) {
	if r == nil {
		return nil, nil
	}
	if len(keys) > MaxPaperDecisions+MaxPaperOutcomes {
		return nil, ErrFeedArchiveCapacity
	}
	if r.Validate() != nil {
		return nil, ErrFeedArchiveConflict
	}
	kinds, ids := make([]string, len(keys)), make([]string, len(keys))
	for i, key := range keys {
		var ok bool
		kinds[i], ids[i], ok = strings.Cut(key, ":")
		if !ok || kinds[i] != "decision" && kinds[i] != "outcome" || !paperaccount.ID.MatchString(ids[i]) {
			return nil, ErrFeedArchiveConflict
		}
	}
	cp, err := readPaperFeedCheckpoint(ctx, tx, r.Checkpoint.Sequence)
	if err != nil {
		return nil, err
	}
	if cp.PaperFeedCheckpointHeader != r.Checkpoint || !reflect.DeepEqual(cp.Prefixes, r.Prefixes) {
		return nil, ErrFeedArchiveConflict
	}
	v := &feedCustodyView{checkpoint: cp, seals: map[string]PaperFeedSourcePin{}}
	rows, err := tx.Query(ctx, paperFeedCustodySealQuery, r.Sequence, kinds, ids)
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
	return m.feedCustodyForOwnersLocked(r, paperFeedCustodyKeys(m.paperDecisions, m.paperOutcomes))
}

func (m *Memory) feedCustodyForOwnersLocked(r *FeedRetention, keys []string) (*feedCustodyView, error) {
	if r == nil {
		return nil, nil
	}
	if len(keys) > MaxPaperDecisions+MaxPaperOutcomes {
		return nil, ErrFeedArchiveCapacity
	}
	if r.Validate() != nil || r.Checkpoint.Sequence > int64(len(m.paperFeedCheckpoints)) {
		return nil, ErrFeedArchiveConflict
	}
	cp := m.paperFeedCheckpoints[r.Checkpoint.Sequence-1]
	if _, err := m.paperArchiveEpochLocked(cp.PaperArchiveSequence); err != nil {
		return nil, err
	}
	if cp.Validate() != nil || cp.PaperFeedCheckpointHeader != r.Checkpoint || !reflect.DeepEqual(cp.Prefixes, r.Prefixes) {
		return nil, ErrFeedArchiveConflict
	}
	seals := map[string]PaperFeedSourcePin{}
	for _, key := range keys {
		kind, id, ok := strings.Cut(key, ":")
		if !ok || kind != "decision" && kind != "outcome" || !paperaccount.ID.MatchString(id) {
			return nil, ErrFeedArchiveConflict
		}
		if pin, ok := m.paperFeedSourceSeals[key]; ok {
			if pin.Kind != kind || pin.ID != id {
				return nil, ErrFeedArchiveConflict
			}
			seals[key] = pin
		}
	}
	return &feedCustodyView{checkpoint: cp, seals: seals}, nil
}
