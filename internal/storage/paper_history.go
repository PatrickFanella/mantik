package storage

import (
	"cmp"
	"context"
	"encoding/base64"
	"encoding/json"
	"slices"

	"git.subcult.tv/subculture-collective/mantik/internal/paperaccount"
	"git.subcult.tv/subculture-collective/mantik/internal/paperstrategy"
	"git.subcult.tv/subculture-collective/mantik/internal/workerplan"
	"github.com/jackc/pgx/v5"
)

const MaxPaperHistoryPage = maxPaperNativePage
const MaxPaperHistoryBytes = 8 << 20

type PaperHistoryQuery struct {
	ReceiverID string
	Kind       string
	After      string
	Limit      int
}

type paperHistoryCursor struct {
	Schema        string `json:"schema"`
	ReceiverID    string `json:"receiver_id"`
	Kind          string `json:"kind"`
	Through       int64  `json:"through"`
	ThroughSHA256 string `json:"through_sha256"`
	CreatedEpoch  int64  `json:"created_epoch"`
	ID            string `json:"id"`
}

func (q PaperHistoryQuery) cursor() (*paperHistoryCursor, error) {
	if !ValidReceiverID(q.ReceiverID) || q.Kind != "decision" && q.Kind != "outcome" || q.Limit < 1 || q.Limit > MaxPaperHistoryPage || len(q.After) > 4096 {
		return nil, ErrPaperRecordArchiveInvalid
	}
	if q.After == "" {
		return nil, nil
	}
	raw, err := base64.RawURLEncoding.Strict().DecodeString(q.After)
	var c paperHistoryCursor
	if err != nil || workerplan.ReadBoundedShape(raw, &c, 3072) != nil || c.Schema != "mantik-paper-history-cursor-v1" || c.ReceiverID != q.ReceiverID || c.Kind != q.Kind || c.Through < 1 || !decisionPin.MatchString(c.ThroughSHA256) || c.CreatedEpoch < 0 || c.CreatedEpoch >= c.Through || !paperaccount.ID.MatchString(c.ID) {
		return nil, ErrPaperRecordArchiveInvalid
	}
	return &c, nil
}

func (q PaperHistoryQuery) Validate() error {
	_, err := q.cursor()
	return err
}

type PaperHistoryRecord struct {
	PaperRecordEpoch
	Decision *PaperDecisionRecord `json:"decision,omitempty"`
	Outcome  *PaperOutcomeRecord  `json:"outcome,omitempty"`
}

type PaperHistoryPage struct {
	Schema              string                    `json:"schema"`
	ReceiverID          string                    `json:"receiver_id"`
	Kind                string                    `json:"kind"`
	Through             *PaperRecordArchiveHeader `json:"through"`
	Records             []PaperHistoryRecord      `json:"records"`
	NextAfter           string                    `json:"next_after,omitempty"`
	Authority           string                    `json:"authority"`
	QualificationCredit bool                      `json:"qualification_credit"`
	SHA256              string                    `json:"sha256"`
}

type PaperHistoryStore interface {
	PaperHistory(context.Context, PaperHistoryQuery, PaperDecisionEvaluator, PaperOutcomeEvaluator) (PaperHistoryPage, error)
}

func paperHistoryOrder(a, b PaperRecordEpoch) int {
	if c := cmp.Compare(a.CreatedEpoch, b.CreatedEpoch); c != 0 {
		return c
	}
	return cmp.Compare(a.ID, b.ID)
}

// Validate checks the presentation envelope. Native source custody is checked
// by the service inside the selection's lock or repeatable-read transaction.
func (p PaperHistoryPage) Validate(q PaperHistoryQuery) error {
	after, err := q.cursor()
	sha := p.SHA256
	p.SHA256 = ""
	if err != nil || p.Schema != "mantik-paper-history-page-v1" || p.ReceiverID != q.ReceiverID || p.Kind != q.Kind || p.Authority != "service_owned_paper_history_only" || p.QualificationCredit || p.Records == nil || len(p.Records) > q.Limit || workerplan.Hash(p) != sha {
		return ErrPaperRecordArchiveInvalid
	}
	if p.Through == nil {
		if after != nil || len(p.Records) != 0 || p.NextAfter != "" {
			return ErrPaperRecordArchiveInvalid
		}
		return nil
	}
	h := p.Through
	if h.Validate() != nil || h.ReceiverID != q.ReceiverID || after != nil && (after.Through != h.Sequence || after.ThroughSHA256 != h.SHA256) {
		return ErrPaperRecordArchiveInvalid
	}
	var previous PaperRecordEpoch
	if after != nil {
		previous = PaperRecordEpoch{ID: after.ID, CreatedEpoch: after.CreatedEpoch}
	}
	for i, r := range p.Records {
		if r.Kind != q.Kind || r.CreatedEpoch < 0 || r.CreatedEpoch >= h.Sequence || !paperaccount.ID.MatchString(r.ID) || (i > 0 || after != nil) && paperHistoryOrder(previous, r.PaperRecordEpoch) >= 0 {
			return ErrPaperRecordArchiveInvalid
		}
		if r.Kind == "decision" {
			d := r.Decision
			if d == nil || r.Outcome != nil || d.Validate() != nil || d.Command.ReceiverID != q.ReceiverID || d.Command.ID != r.ID || d.SHA256 != r.RecordSHA256 || !d.FinalizedAt.Equal(r.At) {
				return ErrPaperRecordArchiveInvalid
			}
		} else {
			o := r.Outcome
			if o == nil || r.Decision != nil || o.Validate() != nil || o.Command.ReceiverID != q.ReceiverID || o.Command.ID != r.ID || o.SHA256 != r.RecordSHA256 || !o.EvaluatedAt.Equal(r.At) {
				return ErrPaperRecordArchiveInvalid
			}
		}
		previous = r.PaperRecordEpoch
	}
	if p.NextAfter != "" {
		nextQuery := q
		nextQuery.After = p.NextAfter
		next, err := nextQuery.cursor()
		if err != nil || len(p.Records) != q.Limit || next.Through != h.Sequence || next.ThroughSHA256 != h.SHA256 || next.CreatedEpoch != previous.CreatedEpoch || next.ID != previous.ID {
			return ErrPaperRecordArchiveInvalid
		}
	}
	return nil
}

// A closed creation interval stays fixed while new records and retirement
// receipts arrive. The cursor binds that original boundary, receiver and kind.
func paperHistoryBoundary(q PaperHistoryQuery, head int64, read func(int64) (PaperRecordArchiveHeader, error)) (*PaperRecordArchiveHeader, *paperHistoryCursor, error) {
	after, err := q.cursor()
	if err != nil {
		return nil, nil, err
	}
	through := head
	if after != nil {
		through = after.Through
	}
	if through > head {
		return nil, nil, ErrPaperRecordArchiveInvalid
	}
	if through == 0 {
		return nil, nil, nil
	}
	h, err := read(through)
	if err != nil {
		return nil, nil, err
	}
	if h.Validate() != nil || h.ReceiverID != q.ReceiverID || h.Sequence != through || after != nil && after.ThroughSHA256 != h.SHA256 {
		return nil, nil, ErrPaperRecordArchiveConflict
	}
	return &h, after, nil
}

func finishPaperHistory(ctx context.Context, q PaperHistoryQuery, h *PaperRecordArchiveHeader, rows []paperSampleRecord, native func([]paperSampleRecord) (paperArchiveOwners, error)) (PaperHistoryPage, error) {
	p := PaperHistoryPage{Schema: "mantik-paper-history-page-v1", ReceiverID: q.ReceiverID, Kind: q.Kind, Through: h, Records: []PaperHistoryRecord{}, Authority: "service_owned_paper_history_only"}
	more := len(rows) > q.Limit
	if more {
		rows = rows[:q.Limit:q.Limit]
	}
	for start := 0; start < len(rows); {
		end := start + 1
		for end < len(rows) && rows[end].CreatedEpoch == rows[start].CreatedEpoch {
			end++
		}
		owners, err := native(rows[start:end])
		if err != nil {
			return PaperHistoryPage{}, err
		}
		for _, r := range rows[start:end] {
			if err := ctx.Err(); err != nil {
				return PaperHistoryPage{}, err
			}
			item := PaperHistoryRecord{PaperRecordEpoch: r.PaperRecordEpoch}
			if q.Kind == "decision" {
				d, ok := owners.decisions[r.ID]
				if !ok {
					return PaperHistoryPage{}, ErrPaperRecordArchiveConflict
				}
				item.Decision = &d.Record
			} else {
				o, ok := owners.outcomes[r.ID]
				if !ok {
					return PaperHistoryPage{}, ErrPaperRecordArchiveConflict
				}
				item.Outcome = &o.Record
			}
			p.Records = append(p.Records, item)
		}
		start = end
	}
	if more {
		r := rows[len(rows)-1]
		raw, err := json.Marshal(paperHistoryCursor{"mantik-paper-history-cursor-v1", q.ReceiverID, q.Kind, h.Sequence, h.SHA256, r.CreatedEpoch, r.ID})
		if err != nil {
			return PaperHistoryPage{}, err
		}
		p.NextAfter = base64.RawURLEncoding.EncodeToString(raw)
	}
	p.SHA256 = workerplan.Hash(p)
	if err := p.Validate(q); err != nil {
		return PaperHistoryPage{}, err
	}
	if raw, err := json.Marshal(p); err != nil || len(raw) > MaxPaperHistoryBytes {
		return PaperHistoryPage{}, ErrPaperRecordArchiveCapacity
	}
	if err := ctx.Err(); err != nil {
		return PaperHistoryPage{}, err
	}
	return clone(p), nil
}

func (m *Memory) PaperHistory(ctx context.Context, q PaperHistoryQuery, de PaperDecisionEvaluator, oe PaperOutcomeEvaluator) (PaperHistoryPage, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := q.Validate(); err != nil || de == nil || oe == nil {
		return PaperHistoryPage{}, ErrPaperRecordArchiveInvalid
	}
	if q.ReceiverID != m.receiverID {
		return PaperHistoryPage{}, ErrReceiverMismatch
	}
	head := int64(len(m.paperRecordArchives))
	h, after, err := paperHistoryBoundary(q, head, m.paperArchiveEpochLocked)
	if err != nil {
		return PaperHistoryPage{}, err
	}
	rows := []paperSampleRecord{}
	if h != nil {
		for key, e := range m.paperRecordEpochs {
			if err := ctx.Err(); err != nil {
				return PaperHistoryPage{}, err
			}
			if e.Kind != q.Kind || e.CreatedEpoch >= h.Sequence || after != nil && paperHistoryOrder(e, PaperRecordEpoch{CreatedEpoch: after.CreatedEpoch, ID: after.ID}) <= 0 {
				continue
			}
			r := paperSampleRecord{e, m.paperRecordScopes[key], m.paperRecordRetiredEpochs[key]}
			if key != e.Kind+":"+e.ID || !validPaperSampleRecord(r, head) {
				return PaperHistoryPage{}, ErrPaperRecordArchiveConflict
			}
			index, _ := slices.BinarySearchFunc(rows, r, func(a, b paperSampleRecord) int { return paperHistoryOrder(a.PaperRecordEpoch, b.PaperRecordEpoch) })
			if index <= q.Limit {
				rows = slices.Insert(rows, index, r)
				if len(rows) > q.Limit+1 {
					rows = rows[:q.Limit+1]
				}
			}
		}
	}
	return finishPaperHistory(ctx, q, h, rows, func(wanted []paperSampleRecord) (paperArchiveOwners, error) {
		a, err := m.paperRecordArchivePayloadLocked(wanted[0].CreatedEpoch + 1)
		if err != nil {
			return paperArchiveOwners{}, err
		}
		return m.paperSampleNativeWithSourcesLocked(ctx, q.ReceiverID, wanted, de, oe, func(keys []string) (FeedEvidenceSnapshot, paperstrategy.Catalog, error) {
			feed := a.Context.Feed
			view, err := m.feedCustodyForOwnersLocked(feed.Retention, keys)
			feed.custody = view
			return feed, a.Context.Strategies, err
		})
	})
}

const paperHistoryPageQuery = `SELECT kind,record_id,created_epoch,record_sha256,at_seconds,at_nanos,
strategy_id,strategy_sha256,decision_id,decision_sha256,decision_at_seconds,decision_at_nanos,coalesce(retired_epoch,0)
FROM mantik_paper_record_epochs
WHERE kind=$1 AND kind IN ('decision','outcome') AND created_epoch<$2
AND (created_epoch,record_id COLLATE "C")>($3::bigint,$4::text COLLATE "C")
ORDER BY created_epoch,record_id COLLATE "C" LIMIT $5`

func (p *Postgres) PaperHistory(ctx context.Context, q PaperHistoryQuery, de PaperDecisionEvaluator, oe PaperOutcomeEvaluator) (PaperHistoryPage, error) {
	if err := q.Validate(); err != nil || de == nil || oe == nil {
		return PaperHistoryPage{}, ErrPaperRecordArchiveInvalid
	}
	tx, err := p.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return PaperHistoryPage{}, err
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	var owner string
	var head int64
	if err := tx.QueryRow(ctx, "SELECT id,(SELECT coalesce(max(sequence),0) FROM mantik_paper_record_archives) FROM mantik_research_receiver WHERE singleton").Scan(&owner, &head); err != nil {
		return PaperHistoryPage{}, err
	}
	if owner != q.ReceiverID {
		return PaperHistoryPage{}, ErrReceiverMismatch
	}
	h, after, err := paperHistoryBoundary(q, head, func(n int64) (PaperRecordArchiveHeader, error) { return readPaperArchiveEpoch(ctx, tx, owner, n) })
	if err != nil {
		return PaperHistoryPage{}, err
	}
	rows := []paperSampleRecord{}
	if h != nil {
		epoch, id := int64(0), ""
		if after != nil {
			epoch, id = after.CreatedEpoch, after.ID
		}
		result, err := tx.Query(ctx, paperHistoryPageQuery, q.Kind, h.Sequence, epoch, id, q.Limit+1)
		if err != nil {
			return PaperHistoryPage{}, err
		}
		for result.Next() {
			r, err := scanPaperSampleRecord(result)
			if err != nil || !validPaperSampleRecord(r, head) {
				result.Close()
				if err != nil {
					return PaperHistoryPage{}, err
				}
				return PaperHistoryPage{}, ErrPaperRecordArchiveConflict
			}
			rows = append(rows, r)
		}
		err = result.Err()
		result.Close()
		if err != nil {
			return PaperHistoryPage{}, err
		}
	}
	out, err := finishPaperHistory(ctx, q, h, rows, func(wanted []paperSampleRecord) (paperArchiveOwners, error) {
		a, err := readPaperRecordArchivePayload(ctx, tx, wanted[0].CreatedEpoch+1)
		if err != nil {
			return paperArchiveOwners{}, err
		}
		return readPaperSampleNativeWithSources(ctx, tx, owner, wanted, de, oe, func(keys []string) (FeedEvidenceSnapshot, paperstrategy.Catalog, error) {
			feed := a.Context.Feed
			view, err := readFeedCustodyForOwners(ctx, tx, feed.Retention, keys)
			feed.custody = view
			return feed, a.Context.Strategies, err
		})
	})
	if err != nil {
		return PaperHistoryPage{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return PaperHistoryPage{}, err
	}
	return out, nil
}
