package storage

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"reflect"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/allocation"
	"git.subcult.tv/subculture-collective/mantik/internal/paperaccount"
	"git.subcult.tv/subculture-collective/mantik/internal/paperstrategy"
	"git.subcult.tv/subculture-collective/mantik/internal/workerplan"
	"github.com/jackc/pgx/v5"
)

const MaxOwnedAllocationHistoryPage = 64
const MaxOwnedAllocationHistoryBytes = 8 << 20

type OwnedAllocationHistoryQuery struct {
	ReceiverID string
	After      string
	Limit      int
}

type allocationHistoryCursor struct {
	Schema           string `json:"schema"`
	ReceiverID       string `json:"receiver_id"`
	ThroughSequence  int    `json:"through_sequence"`
	CheckpointSHA256 string `json:"checkpoint_sha256"`
	AfterSequence    int    `json:"after_sequence"`
}

func (q OwnedAllocationHistoryQuery) cursor() (*allocationHistoryCursor, error) {
	if !ValidReceiverID(q.ReceiverID) || q.Limit < 1 || q.Limit > MaxOwnedAllocationHistoryPage || len(q.After) > 4096 {
		return nil, ErrOwnedAllocationInvalid
	}
	if q.After == "" {
		return nil, nil
	}
	raw, err := base64.RawURLEncoding.Strict().DecodeString(q.After)
	var c allocationHistoryCursor
	if err != nil || workerplan.ReadBoundedShape(raw, &c, 3072) != nil || c.Schema != "mantik-allocation-history-cursor-v1" || c.ReceiverID != q.ReceiverID || c.ThroughSequence < 1 || !decisionPin.MatchString(c.CheckpointSHA256) || c.AfterSequence < 1 || c.AfterSequence >= c.ThroughSequence {
		return nil, ErrOwnedAllocationInvalid
	}
	return &c, nil
}

func (q OwnedAllocationHistoryQuery) Validate() error {
	_, err := q.cursor()
	return err
}

type OwnedAllocationHistoryRecord struct {
	Sequence     int                    `json:"sequence"`
	Kind         string                 `json:"kind"`
	RecordSHA256 string                 `json:"record_sha256"`
	Comparison   *OwnedAllocationRecord `json:"comparison,omitempty"`
	Execution    *OwnedExecutionRecord  `json:"execution,omitempty"`
}

type OwnedAllocationHistoryPage struct {
	Schema              string                         `json:"schema"`
	ReceiverID          string                         `json:"receiver_id"`
	ThroughSequence     int                            `json:"through_sequence"`
	CheckpointSHA256    string                         `json:"checkpoint_sha256,omitempty"`
	Records             []OwnedAllocationHistoryRecord `json:"records"`
	NextAfter           string                         `json:"next_after,omitempty"`
	Scope               string                         `json:"scope"`
	Authority           string                         `json:"authority"`
	QualificationCredit bool                           `json:"qualification_credit"`
	SHA256              string                         `json:"sha256"`
}

type OwnedAllocationHistoryStore interface {
	OwnedAllocationHistory(context.Context, OwnedAllocationHistoryQuery, PaperDecisionEvaluator, ...PaperOutcomeEvaluator) (OwnedAllocationHistoryPage, error)
}

// Validate checks the presentation envelope. Original membership and native
// replay belong to the service's lock or repeatable-read transaction.
func (p OwnedAllocationHistoryPage) Validate(q OwnedAllocationHistoryQuery) error {
	after, err := q.cursor()
	sha := p.SHA256
	p.SHA256 = ""
	if err != nil || p.Schema != "mantik-allocation-history-page-v1" || p.ReceiverID != q.ReceiverID || p.Scope != "indexed_sealed_allocation_history_only" || p.Authority != "service_owned_allocation_history_only" || p.QualificationCredit || p.Records == nil || len(p.Records) > q.Limit || workerplan.Hash(p) != sha || p.ThroughSequence < 0 {
		return ErrOwnedAllocationInvalid
	}
	if p.ThroughSequence == 0 {
		if after != nil || len(p.Records) != 0 || p.NextAfter != "" || p.CheckpointSHA256 != "" {
			return ErrOwnedAllocationInvalid
		}
		return nil
	}
	if !decisionPin.MatchString(p.CheckpointSHA256) || after != nil && (after.ThroughSequence != p.ThroughSequence || after.CheckpointSHA256 != p.CheckpointSHA256) {
		return ErrOwnedAllocationInvalid
	}
	sequence := 0
	if after != nil {
		sequence = after.AfterSequence
	}
	for _, r := range p.Records {
		sequence++
		if r.Sequence != sequence || sequence > p.ThroughSequence || !decisionPin.MatchString(r.RecordSHA256) {
			return ErrOwnedAllocationInvalid
		}
		switch r.Kind {
		case "comparison":
			if r.Comparison == nil || r.Execution != nil {
				return ErrOwnedAllocationInvalid
			}
			c := *r.Comparison
			c.SHA256 = ""
			if c.Command.Validate() != nil || c.Command.ReceiverID != q.ReceiverID || r.Comparison.SHA256 != r.RecordSHA256 || workerplan.Hash(c) != r.RecordSHA256 {
				return ErrOwnedAllocationInvalid
			}
		case "execution":
			if r.Execution == nil || r.Comparison != nil {
				return ErrOwnedAllocationInvalid
			}
			e := *r.Execution
			e.SHA256 = ""
			if e.Schema != OwnedExecutionStepSchema || e.Command.ReceiverID != q.ReceiverID || !paperaccount.ID.MatchString(e.Command.ID) || r.Execution.SHA256 != r.RecordSHA256 || workerplan.Hash(e) != r.RecordSHA256 {
				return ErrOwnedAllocationInvalid
			}
		default:
			return ErrOwnedAllocationInvalid
		}
	}
	if sequence < p.ThroughSequence {
		nextQuery := q
		nextQuery.After = p.NextAfter
		next, err := nextQuery.cursor()
		if err != nil || next == nil || len(p.Records) != q.Limit || next.ThroughSequence != p.ThroughSequence || next.CheckpointSHA256 != p.CheckpointSHA256 || next.AfterSequence != sequence {
			return ErrOwnedAllocationInvalid
		}
	} else if p.NextAfter != "" {
		return ErrOwnedAllocationInvalid
	}
	return nil
}

type allocationHistorySelection struct {
	page     OwnedAllocationHistoryPage
	members  []ownedAllocationMember
	segments []ownedAllocationSegment
}

func selectAllocationHistory(ctx context.Context, q OwnedAllocationHistoryQuery, current *OwnedAllocationCheckpoint, checkpoint func(int) (OwnedAllocationCheckpoint, error), members func(int, int) ([]ownedAllocationMember, error), segment func(int) (ownedAllocationSegment, error)) (allocationHistorySelection, error) {
	out := allocationHistorySelection{page: OwnedAllocationHistoryPage{Schema: "mantik-allocation-history-page-v1", ReceiverID: q.ReceiverID, Records: []OwnedAllocationHistoryRecord{}, Scope: "indexed_sealed_allocation_history_only", Authority: "service_owned_allocation_history_only"}}
	after, err := q.cursor()
	if err != nil {
		return allocationHistorySelection{}, err
	}
	if current == nil {
		if after != nil {
			return allocationHistorySelection{}, ErrOwnedAllocationInvalid
		}
		return out, ctx.Err()
	}
	through, start := current.Sequence, 0
	if after != nil {
		through, start = after.ThroughSequence, after.AfterSequence
	}
	if through < 1 || through > current.Sequence {
		return allocationHistorySelection{}, ErrOwnedAllocationInvalid
	}
	original, err := checkpoint(through)
	if err != nil {
		return allocationHistorySelection{}, err
	}
	unsigned := original
	unsigned.SHA256 = ""
	if original.Sequence != through || !decisionPin.MatchString(original.SHA256) || workerplan.Hash(unsigned) != original.SHA256 || after != nil && after.CheckpointSHA256 != original.SHA256 || after == nil && !reflect.DeepEqual(original, *current) {
		return allocationHistorySelection{}, ErrOwnedAllocationConflict
	}
	out.page.ThroughSequence, out.page.CheckpointSHA256 = through, original.SHA256
	end := start + min(q.Limit, through-start)
	out.members, err = members(start, end)
	if err != nil {
		return allocationHistorySelection{}, err
	}
	if len(out.members) != end-start {
		// A legacy or missing index must not become a successful partial page.
		return allocationHistorySelection{}, ErrOwnedAllocationConflict
	}
	segmentBytes := 0
	for i, member := range out.members {
		if err := ctx.Err(); err != nil {
			return allocationHistorySelection{}, err
		}
		if member.Sequence != start+i+1 {
			return allocationHistorySelection{}, ErrOwnedAllocationConflict
		}
		if len(out.segments) == 0 || out.segments[len(out.segments)-1].After.Sequence < member.Sequence {
			s, err := segment(member.Sequence)
			if err != nil {
				return allocationHistorySelection{}, err
			}
			if err := allocationTargetDependencies(ownedAllocationJournal{Records: s.Records}); err != nil {
				return allocationHistorySelection{}, err
			}
			raw, err := json.Marshal(s)
			if err != nil || len(raw) > MaxOwnedAllocationBytes-segmentBytes {
				return allocationHistorySelection{}, ErrOwnedAllocationCapacity
			}
			segmentBytes += len(raw)
			out.segments = append(out.segments, s)
		}
		if member.CheckpointSequence != out.segments[len(out.segments)-1].After.Sequence {
			return allocationHistorySelection{}, ErrOwnedAllocationConflict
		}
	}
	if end < through {
		raw, err := json.Marshal(allocationHistoryCursor{"mantik-allocation-history-cursor-v1", q.ReceiverID, through, original.SHA256, end})
		if err != nil {
			return allocationHistorySelection{}, err
		}
		out.page.NextAfter = base64.RawURLEncoding.EncodeToString(raw)
	}
	return out, nil
}

func finishAllocationHistory(ctx context.Context, q OwnedAllocationHistoryQuery, selected allocationHistorySelection, native func(ownedAllocationSegment) (OwnedAllocationCatalog, error)) (OwnedAllocationHistoryPage, error) {
	p := selected.page
	index := 0
	for _, segment := range selected.segments {
		catalog, err := native(segment)
		if err != nil {
			return OwnedAllocationHistoryPage{}, err
		}
		for index < len(selected.members) && selected.members[index].Sequence <= segment.After.Sequence {
			member := selected.members[index]
			index++
			r := OwnedAllocationHistoryRecord{Sequence: member.Sequence, Kind: member.Kind, RecordSHA256: member.RecordSHA256}
			if member.Kind == "comparison" {
				c, err := allocationComparisonFromCatalog(member.CommandID, catalog)
				if err != nil {
					return OwnedAllocationHistoryPage{}, err
				}
				r.Comparison = &c
			} else if member.Kind == "execution" {
				for _, e := range catalog.Executions {
					if e.Command.ID == member.CommandID {
						e := clone(e)
						r.Execution = &e
						break
					}
				}
			}
			p.Records = append(p.Records, r)
		}
	}
	p.SHA256 = workerplan.Hash(p)
	if err := p.Validate(q); err != nil {
		return OwnedAllocationHistoryPage{}, err
	}
	if raw, err := json.Marshal(p); err != nil || len(raw) > MaxOwnedAllocationHistoryBytes {
		return OwnedAllocationHistoryPage{}, ErrOwnedAllocationCapacity
	}
	if err := ctx.Err(); err != nil {
		return OwnedAllocationHistoryPage{}, err
	}
	return clone(p), nil
}

func (m *Memory) OwnedAllocationHistory(ctx context.Context, q OwnedAllocationHistoryQuery, de PaperDecisionEvaluator, oe ...PaperOutcomeEvaluator) (OwnedAllocationHistoryPage, error) {
	if q.Validate() != nil || de == nil {
		return OwnedAllocationHistoryPage{}, ErrOwnedAllocationInvalid
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if q.ReceiverID != m.receiverID {
		return OwnedAllocationHistoryPage{}, ErrReceiverMismatch
	}
	if m.ownedAllocations.Checkpoint == nil && (len(m.ownedAllocationArchive) != 0 || len(m.ownedAllocationIndex.Members) != 0 || len(m.ownedAllocationIndex.Checkpoints) != 0 || len(m.ownedAllocationIndex.Batches) != 0) {
		return OwnedAllocationHistoryPage{}, ErrOwnedAllocationConflict
	}
	selected, err := selectAllocationHistory(ctx, q, m.ownedAllocations.Checkpoint, func(n int) (OwnedAllocationCheckpoint, error) {
		c, ok := m.ownedAllocationIndex.Checkpoints[n]
		if !ok {
			return OwnedAllocationCheckpoint{}, ErrOwnedAllocationConflict
		}
		return c, nil
	}, func(start, end int) ([]ownedAllocationMember, error) {
		rows := []ownedAllocationMember{}
		for n := start + 1; n <= end; n++ {
			if r, ok := m.ownedAllocationIndex.Members[n]; ok {
				rows = append(rows, r)
			}
		}
		return rows, nil
	}, func(n int) (ownedAllocationSegment, error) { return m.ownedAllocationSegmentLocked(ctx, n) })
	if err != nil {
		return OwnedAllocationHistoryPage{}, err
	}
	return finishAllocationHistory(ctx, q, selected, func(s ownedAllocationSegment) (OwnedAllocationCatalog, error) {
		return m.replayAllocationReadLocked(ctx, ownedAllocationJournal{Checkpoint: s.Before, Records: s.Records}, &s, de, oe...)
	})
}

func (p *Postgres) OwnedAllocationHistory(ctx context.Context, q OwnedAllocationHistoryQuery, de PaperDecisionEvaluator, oe ...PaperOutcomeEvaluator) (OwnedAllocationHistoryPage, error) {
	if q.Validate() != nil || de == nil {
		return OwnedAllocationHistoryPage{}, ErrOwnedAllocationInvalid
	}
	var selected allocationHistorySelection
	var out OwnedAllocationHistoryPage
	var snapshot pgx.Tx
	_, _, err := p.ownedAllocationAccessFromJournal(ctx, false, nil, func(ctx context.Context, owner string, j ownedAllocationJournal, policies allocation.PolicyCatalog, decisions PaperDecisionCatalog, strategies paperstrategy.Catalog, retained map[string]retainedPaperDecision, x *ownedExecution, _ time.Time, declarations PaperEvaluationDeclarationCatalog, assessments PaperEvaluationAssessmentCatalog) (ownedAllocationJournal, bool, error) {
		if owner != q.ReceiverID {
			return ownedAllocationJournal{}, false, ErrReceiverMismatch
		}
		var err error
		out, err = finishAllocationHistory(ctx, q, selected, func(s ownedAllocationSegment) (OwnedAllocationCatalog, error) {
			j := ownedAllocationJournal{Checkpoint: s.Before, Records: s.Records}
			d, r, err := readAllocationOwnerDecisions(ctx, snapshot, decisions, retained, j, nil, de)
			if err != nil {
				return OwnedAllocationCatalog{}, err
			}
			sources := paperEvaluationSources{policies: policies, decisions: d, strategies: strategies, retained: r, declarations: declarations}
			return replayOwnedAllocationSegment(ctx, s, sources, x, assessments)
		})
		return j, false, err
	}, func(ctx context.Context, tx pgx.Tx, j ownedAllocationJournal) (ownedAllocationJournal, error) {
		snapshot = tx
		if j.Checkpoint == nil {
			var exists bool
			if err := tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM mantik_owned_allocation_archive) OR EXISTS(SELECT 1 FROM mantik_owned_allocation_members) OR EXISTS(SELECT 1 FROM mantik_owned_allocation_checkpoints) OR EXISTS(SELECT 1 FROM mantik_owned_allocation_batches)").Scan(&exists); err != nil {
				return ownedAllocationJournal{}, err
			}
			if exists {
				return ownedAllocationJournal{}, ErrOwnedAllocationConflict
			}
		}
		var err error
		selected, err = selectAllocationHistory(ctx, q, j.Checkpoint, func(n int) (OwnedAllocationCheckpoint, error) {
			var raw []byte
			var sha string
			if err := tx.QueryRow(ctx, "SELECT payload,sha256 FROM mantik_owned_allocation_checkpoints WHERE sequence=$1", n).Scan(&raw, &sha); err != nil {
				if err == pgx.ErrNoRows {
					err = ErrOwnedAllocationConflict
				}
				return OwnedAllocationCheckpoint{}, err
			}
			var c OwnedAllocationCheckpoint
			if workerplan.ReadBoundedShape(raw, &c, MaxOwnedAllocationBytes) != nil || c.SHA256 != sha {
				return OwnedAllocationCheckpoint{}, ErrOwnedAllocationConflict
			}
			return c, nil
		}, func(start, end int) ([]ownedAllocationMember, error) {
			rows, err := tx.Query(ctx, "SELECT sequence,checkpoint_sequence,command_id,kind,record_sha256,item_sha256 FROM mantik_owned_allocation_members WHERE sequence>$1 AND sequence<=$2 ORDER BY sequence LIMIT $3", start, end, q.Limit)
			if err != nil {
				return nil, err
			}
			defer rows.Close()
			result := []ownedAllocationMember{}
			for rows.Next() {
				var r ownedAllocationMember
				if err := rows.Scan(&r.Sequence, &r.CheckpointSequence, &r.CommandID, &r.Kind, &r.RecordSHA256, &r.ItemSHA256); err != nil {
					return nil, err
				}
				result = append(result, r)
			}
			return result, rows.Err()
		}, func(n int) (ownedAllocationSegment, error) { return readOwnedAllocationSegment(ctx, tx, n) })
		return ownedAllocationJournal{Records: []retainedOwnedAllocation{}}, err
	}, de, oe...)
	if err != nil {
		return OwnedAllocationHistoryPage{}, err
	}
	return out, nil
}
