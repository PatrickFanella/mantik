package storage

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// CreatedEpoch is the last committed paper archive when the record was first
// inserted. It closes logical membership without relying on rounded SQL clocks.
type PaperRecordEpoch struct {
	Kind         string    `json:"kind"`
	ID           string    `json:"id"`
	CreatedEpoch int64     `json:"created_epoch"`
	RecordSHA256 string    `json:"record_sha256"`
	At           time.Time `json:"at"`
}

func (m *Memory) retainPaperRecordEpoch(kind, id, sha string, at time.Time) {
	if m.paperRecordEpochs == nil {
		m.paperRecordEpochs = map[string]PaperRecordEpoch{}
	}
	m.paperRecordEpochs[kind+":"+id] = PaperRecordEpoch{kind, id, int64(len(m.paperRecordArchives)), sha, at.UTC()}
}

func readPaperRecordEpochs(ctx context.Context, tx pgx.Tx, keys []string) (map[string]PaperRecordEpoch, error) {
	if len(keys) > maxPaperArchiveRecords {
		return nil, ErrPaperRecordArchiveCapacity
	}
	kinds, ids := make([]string, len(keys)), make([]string, len(keys))
	for i, key := range keys {
		var ok bool
		kinds[i], ids[i], ok = strings.Cut(key, ":")
		if !ok || !paperRecordKind(kinds[i]) {
			return nil, ErrPaperRecordArchiveInvalid
		}
	}
	rows, err := tx.Query(ctx, "SELECT e.kind,e.record_id,e.created_epoch,e.record_sha256,e.at_seconds,e.at_nanos FROM unnest($1::text[],$2::text[]) AS w(kind,id) JOIN LATERAL (SELECT * FROM mantik_paper_record_epochs WHERE kind=w.kind AND record_id=w.id LIMIT 1) e ON true ORDER BY e.kind,e.record_id", kinds, ids)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]PaperRecordEpoch{}
	for rows.Next() {
		var e PaperRecordEpoch
		var seconds int64
		var nanos int
		if err := rows.Scan(&e.Kind, &e.ID, &e.CreatedEpoch, &e.RecordSHA256, &seconds, &nanos); err != nil {
			return nil, err
		}
		e.At = time.Unix(seconds, int64(nanos)).UTC()
		out[e.Kind+":"+e.ID] = e
	}
	return out, rows.Err()
}

func checkPaperArchiveEpochs(sequence int64, records []ArchivedPaperRecord, epochs map[string]PaperRecordEpoch, retired map[string]int64) error {
	expected := 0
	for key, e := range epochs {
		if paperOwnerAtEpoch(e.CreatedEpoch, retired[key], sequence-1) {
			expected++
		}
	}
	if expected != len(records) {
		return ErrPaperRecordArchiveConflict
	}
	for _, r := range records {
		if e, ok := epochs[r.Kind+":"+r.ID]; !ok || e != r.PaperRecordEpoch || !paperOwnerAtEpoch(e.CreatedEpoch, retired[r.Kind+":"+r.ID], sequence-1) {
			return ErrPaperRecordArchiveConflict
		}
	}
	return nil
}

func readPaperArchiveEpoch(ctx context.Context, tx pgx.Tx, owner string, epoch int64) (PaperRecordArchiveHeader, error) {
	if epoch == 0 {
		return PaperRecordArchiveHeader{}, nil
	}
	h, err := readPaperRecordArchiveHeader(ctx, tx, epoch)
	if errors.Is(err, ErrPaperRecordArchiveNotFound) || errors.Is(err, ErrPaperRecordArchiveConflict) {
		return PaperRecordArchiveHeader{}, ErrFeedArchiveConflict
	}
	if err != nil {
		return PaperRecordArchiveHeader{}, err
	}
	if h.ReceiverID != owner {
		return PaperRecordArchiveHeader{}, ErrFeedArchiveConflict
	}
	return h, nil
}

func (m *Memory) paperArchiveEpochLocked(epoch int64) (PaperRecordArchiveHeader, error) {
	if epoch == 0 {
		return PaperRecordArchiveHeader{}, nil
	}
	if epoch < 0 || epoch > int64(len(m.paperRecordArchives)) || epoch > int64(len(m.paperArchiveSeals)) {
		return PaperRecordArchiveHeader{}, ErrFeedArchiveConflict
	}
	h := m.paperRecordArchives[epoch-1].PaperRecordArchiveHeader
	if h.Validate() != nil || h.ReceiverID != m.receiverID || h != m.paperArchiveSeals[epoch-1] {
		return PaperRecordArchiveHeader{}, ErrFeedArchiveConflict
	}
	return h, nil
}
