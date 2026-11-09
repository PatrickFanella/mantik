package storage

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/workerplan"
	"github.com/jackc/pgx/v5"
)

// Retirement is separate from PaperRecordEpoch: moving an owner must never
// change the creation pin already hashed into an original archive.
func paperOwnerAtEpoch(created, retired, epoch int64) bool {
	return created >= 0 && created <= epoch && (retired == 0 || retired > epoch)
}

type paperCheckpointOwner struct {
	PaperRecordEpoch
	retired int64
	raw     []byte
}

// Membership comes from independently reserved owner metadata, never a caller's
// manifest or the list of records that happen to remain in the active tables.
func paperCheckpointOwners(ctx context.Context, owners []paperCheckpointOwner, head int64, original func(int64) (PaperRecordArchive, error)) (paperArchiveOwners, error) {
	out := paperArchiveOwners{decisions: map[string]retainedPaperDecision{}, outcomes: map[string]retainedPaperOutcome{}}
	if len(owners) > MaxPaperDecisions+MaxPaperOutcomes {
		return out, ErrFeedArchiveConflict
	}
	slices.SortFunc(owners, func(a, b paperCheckpointOwner) int {
		if a.CreatedEpoch < b.CreatedEpoch {
			return -1
		}
		if a.CreatedEpoch > b.CreatedEpoch {
			return 1
		}
		return 0
	})
	var cached PaperRecordArchive
	bytes := 0
	for _, o := range owners {
		if err := ctx.Err(); err != nil {
			return out, err
		}
		if o.CreatedEpoch < 0 || o.CreatedEpoch > head || o.retired != 0 && (o.retired <= o.CreatedEpoch || o.retired > head) {
			return out, ErrFeedArchiveConflict
		}
		raw := o.raw
		if raw == nil {
			// An accidentally missing active row is not an approved retirement.
			if o.retired == 0 || o.CreatedEpoch >= head {
				return out, ErrFeedArchiveConflict
			}
			sequence := o.CreatedEpoch + 1
			if cached.Sequence != sequence {
				var err error
				cached, err = original(sequence)
				if err != nil {
					if errors.Is(err, ErrPaperRecordArchiveNotFound) || errors.Is(err, ErrPaperRecordArchiveConflict) {
						err = ErrFeedArchiveConflict
					}
					return out, err
				}
			}
			for _, r := range cached.Records {
				if r.Kind == o.Kind && r.ID == o.ID {
					if r.PaperRecordEpoch != o.PaperRecordEpoch {
						return out, ErrFeedArchiveConflict
					}
					raw = r.Payload
					break
				}
			}
			if raw == nil {
				return out, ErrFeedArchiveConflict
			}
		}
		bytes += len(raw)
		if bytes > MaxPaperRecordArchiveBytes {
			return out, ErrFeedArchiveConflict
		}
		switch o.Kind {
		case "decision":
			var d retainedPaperDecision
			if workerplan.ReadBoundedShape(raw, &d, MaxPaperDecisionBytes) != nil || d.Record.Validate() != nil || d.Record.Command.ID != o.ID || d.Record.SHA256 != o.RecordSHA256 || !d.Record.FinalizedAt.Equal(o.At) {
				return out, ErrFeedArchiveConflict
			}
			out.decisions[o.ID] = d
		case "outcome":
			var v retainedPaperOutcome
			if workerplan.ReadBoundedShape(raw, &v, MaxPaperOutcomeBytes) != nil || v.Record.Validate() != nil || v.Record.Command.ID != o.ID || v.Record.SHA256 != o.RecordSHA256 || !v.Record.EvaluatedAt.Equal(o.At) {
				return out, ErrFeedArchiveConflict
			}
			out.outcomes[o.ID] = v
		default:
			return out, ErrFeedArchiveConflict
		}
	}
	if len(out.decisions) > MaxPaperDecisions || len(out.outcomes) > MaxPaperOutcomes {
		return out, ErrFeedArchiveConflict
	}
	return out, nil
}

func (m *Memory) paperCheckpointOwnersLocked(ctx context.Context, epoch int64, cut time.Time) (paperArchiveOwners, error) {
	owners := []paperCheckpointOwner{}
	bytes := 0
	for key, e := range m.paperRecordEpochs {
		if e.Kind != "decision" && e.Kind != "outcome" || !paperOwnerAtEpoch(e.CreatedEpoch, m.paperRecordRetiredEpochs[key], epoch) || e.At.After(cut) {
			continue
		}
		var raw []byte
		var err error
		if e.Kind == "decision" {
			if r, ok := m.paperDecisions[e.ID]; ok {
				raw, err = json.Marshal(r)
			}
		} else if r, ok := m.paperOutcomes[e.ID]; ok {
			raw, err = json.Marshal(r)
		}
		if err != nil {
			return paperArchiveOwners{}, err
		}
		bytes += len(raw)
		if len(owners) >= MaxPaperDecisions+MaxPaperOutcomes || bytes > MaxPaperRecordArchiveBytes || key != e.Kind+":"+e.ID {
			return paperArchiveOwners{}, ErrFeedArchiveConflict
		}
		owners = append(owners, paperCheckpointOwner{e, m.paperRecordRetiredEpochs[key], raw})
	}
	return paperCheckpointOwners(ctx, owners, int64(len(m.paperRecordArchives)), m.paperRecordArchivePayloadLocked)
}

const paperCheckpointOwnerQuery = `SELECT e.kind,e.record_id,e.created_epoch,e.record_sha256,e.at_seconds,e.at_nanos,coalesce(e.retired_epoch,0),
CASE e.kind WHEN 'decision' THEN (SELECT payload FROM mantik_paper_decisions WHERE id=e.record_id)
            WHEN 'outcome' THEN (SELECT payload FROM mantik_paper_outcomes WHERE id=e.record_id) END
FROM mantik_paper_record_epochs e
WHERE int8range(e.created_epoch,e.retired_epoch,'[)') @> $1::bigint
AND e.kind IN ('decision','outcome') AND (e.at_seconds,e.at_nanos)<=($2::bigint,$3::integer)
LIMIT $4`

func readPaperCheckpointOwners(ctx context.Context, tx pgx.Tx, epoch int64, cut time.Time) (paperArchiveOwners, error) {
	var head int64
	if err := tx.QueryRow(ctx, "SELECT coalesce(max(sequence),0) FROM mantik_paper_record_archives").Scan(&head); err != nil {
		return paperArchiveOwners{}, err
	}
	rows, err := tx.Query(ctx, paperCheckpointOwnerQuery, epoch, cut.Unix(), cut.Nanosecond(), MaxPaperDecisions+MaxPaperOutcomes+1)
	if err != nil {
		return paperArchiveOwners{}, err
	}
	defer rows.Close()
	owners := []paperCheckpointOwner{}
	bytes := 0
	for rows.Next() {
		var o paperCheckpointOwner
		var seconds int64
		var nanos int
		if err := rows.Scan(&o.Kind, &o.ID, &o.CreatedEpoch, &o.RecordSHA256, &seconds, &nanos, &o.retired, &o.raw); err != nil {
			return paperArchiveOwners{}, err
		}
		o.At = time.Unix(seconds, int64(nanos)).UTC()
		bytes += len(o.raw)
		if len(owners) >= MaxPaperDecisions+MaxPaperOutcomes || bytes > MaxPaperRecordArchiveBytes {
			return paperArchiveOwners{}, ErrFeedArchiveConflict
		}
		owners = append(owners, o)
	}
	if err := rows.Err(); err != nil {
		return paperArchiveOwners{}, err
	}
	rows.Close()
	return paperCheckpointOwners(ctx, owners, head, func(sequence int64) (PaperRecordArchive, error) {
		return readPaperRecordArchivePayload(ctx, tx, sequence)
	})
}
