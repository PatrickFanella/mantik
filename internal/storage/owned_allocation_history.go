package storage

import (
	"cmp"
	"context"
	"encoding/base64"
	"encoding/json"
	"reflect"
	"slices"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/allocation"
	"git.subcult.tv/subculture-collective/mantik/internal/paperaccount"
	"git.subcult.tv/subculture-collective/mantik/internal/paperstrategy"
	"git.subcult.tv/subculture-collective/mantik/internal/workerplan"
	"github.com/jackc/pgx/v5"
)

const MaxOwnedAllocationHistoryPage = 64
const MaxOwnedAllocationHistoryBytes = 8 << 20

const allocationDecisionHistoryQuery = `SELECT m.sequence,m.checkpoint_sequence,m.command_id,m.kind,m.record_sha256,m.item_sha256
FROM mantik_owned_allocation_financial_refs f JOIN mantik_owned_allocation_members m USING(sequence)
WHERE f.sequence>$1 AND f.sequence<=$2 AND f.decision_id COLLATE "C"=$3::text COLLATE "C"
ORDER BY f.sequence LIMIT $4`

type OwnedAllocationHistoryQuery struct {
	ReceiverID string
	After      string
	Limit      int
	DecisionID string
}

type allocationHistoryCursor struct {
	Schema           string `json:"schema"`
	ReceiverID       string `json:"receiver_id"`
	ThroughSequence  int    `json:"through_sequence"`
	CheckpointSHA256 string `json:"checkpoint_sha256"`
	AfterSequence    int    `json:"after_sequence"`
	DecisionID       string `json:"decision_id,omitempty"`
}

func (q OwnedAllocationHistoryQuery) version() string {
	if q.DecisionID != "" {
		return "v2"
	}
	return "v1"
}

func (q OwnedAllocationHistoryQuery) cursor() (*allocationHistoryCursor, error) {
	if !ValidReceiverID(q.ReceiverID) || q.Limit < 1 || q.Limit > MaxOwnedAllocationHistoryPage || len(q.After) > 4096 || q.DecisionID != "" && !paperaccount.ID.MatchString(q.DecisionID) {
		return nil, ErrOwnedAllocationInvalid
	}
	if q.After == "" {
		return nil, nil
	}
	raw, err := base64.RawURLEncoding.Strict().DecodeString(q.After)
	var c allocationHistoryCursor
	if err != nil || workerplan.ReadBoundedShape(raw, &c, 3072) != nil || c.Schema != "mantik-allocation-history-cursor-"+q.version() || c.ReceiverID != q.ReceiverID || c.DecisionID != q.DecisionID || c.ThroughSequence < 1 || !decisionPin.MatchString(c.CheckpointSHA256) || c.AfterSequence < 1 || c.AfterSequence >= c.ThroughSequence {
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
	DecisionID          string                         `json:"decision_id,omitempty"`
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
	if err != nil || p.Schema != "mantik-allocation-history-page-"+q.version() || p.ReceiverID != q.ReceiverID || p.DecisionID != q.DecisionID || p.Scope != "indexed_sealed_allocation_history_only" || p.Authority != "service_owned_allocation_history_only" || p.QualificationCredit || p.Records == nil || len(p.Records) > q.Limit || workerplan.Hash(p) != sha || p.ThroughSequence < 0 {
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
		if r.Sequence <= sequence || r.Sequence > p.ThroughSequence || q.DecisionID == "" && r.Sequence != sequence+1 || !decisionPin.MatchString(r.RecordSHA256) {
			return ErrOwnedAllocationInvalid
		}
		sequence = r.Sequence
		var original retainedOwnedAllocation
		switch r.Kind {
		case "comparison":
			if r.Comparison == nil || r.Execution != nil {
				return ErrOwnedAllocationInvalid
			}
			c := *r.Comparison
			original.Record = c
			c.SHA256 = ""
			if c.Command.Validate() != nil || c.Command.ReceiverID != q.ReceiverID || r.Comparison.SHA256 != r.RecordSHA256 || workerplan.Hash(c) != r.RecordSHA256 {
				return ErrOwnedAllocationInvalid
			}
		case "execution":
			if r.Execution == nil || r.Comparison != nil {
				return ErrOwnedAllocationInvalid
			}
			e := *r.Execution
			original.Execution = &e
			e.SHA256 = ""
			if e.Schema != OwnedExecutionStepSchema || e.Command.ReceiverID != q.ReceiverID || !paperaccount.ID.MatchString(e.Command.ID) || r.Execution.SHA256 != r.RecordSHA256 || workerplan.Hash(e) != r.RecordSHA256 {
				return ErrOwnedAllocationInvalid
			}
		default:
			return ErrOwnedAllocationInvalid
		}
		if q.DecisionID != "" {
			ids, err := allocationFinancialDecisions(original)
			if err != nil || !slices.Contains(ids, q.DecisionID) {
				return ErrOwnedAllocationInvalid
			}
		}
	}
	if p.NextAfter != "" || q.DecisionID == "" && sequence < p.ThroughSequence {
		nextQuery := q
		nextQuery.After = p.NextAfter
		next, err := nextQuery.cursor()
		if err != nil || next == nil || len(p.Records) != q.Limit || next.ThroughSequence != p.ThroughSequence || next.CheckpointSHA256 != p.CheckpointSHA256 || next.AfterSequence != sequence {
			return ErrOwnedAllocationInvalid
		}
	}
	return nil
}

type allocationHistorySelection struct {
	page     OwnedAllocationHistoryPage
	members  []ownedAllocationMember
	segments []ownedAllocationSegment
}

func selectAllocationHistory(ctx context.Context, q OwnedAllocationHistoryQuery, current *OwnedAllocationCheckpoint, checkpoint func(int) (OwnedAllocationCheckpoint, error), members func(int, int) ([]ownedAllocationMember, error), segment func(int) (ownedAllocationSegment, error)) (allocationHistorySelection, error) {
	out := allocationHistorySelection{page: OwnedAllocationHistoryPage{Schema: "mantik-allocation-history-page-" + q.version(), ReceiverID: q.ReceiverID, DecisionID: q.DecisionID, Records: []OwnedAllocationHistoryRecord{}, Scope: "indexed_sealed_allocation_history_only", Authority: "service_owned_allocation_history_only"}}
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
	if q.DecisionID != "" {
		end = through
	}
	out.members, err = members(start, end)
	if err != nil {
		return allocationHistorySelection{}, err
	}
	more := end < through
	if q.DecisionID == "" && len(out.members) != end-start || q.DecisionID != "" && len(out.members) > q.Limit+1 {
		// A legacy or missing index must not become a successful partial page.
		return allocationHistorySelection{}, ErrOwnedAllocationConflict
	}
	if q.DecisionID != "" {
		more = len(out.members) > q.Limit
		if more {
			out.members = out.members[:q.Limit:q.Limit]
		}
	}
	segmentBytes := 0
	previous := start
	for i, member := range out.members {
		if err := ctx.Err(); err != nil {
			return allocationHistorySelection{}, err
		}
		if member.Sequence <= previous || member.Sequence > through || q.DecisionID == "" && member.Sequence != start+i+1 {
			return allocationHistorySelection{}, ErrOwnedAllocationConflict
		}
		previous = member.Sequence
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
	if more {
		raw, err := json.Marshal(allocationHistoryCursor{Schema: "mantik-allocation-history-cursor-" + q.version(), ReceiverID: q.ReceiverID, ThroughSequence: through, CheckpointSHA256: original.SHA256, AfterSequence: previous, DecisionID: q.DecisionID})
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
	return m.allocationHistoryPageLocked(ctx, q, de, oe...)
}

func (m *Memory) allocationHistoryPageLocked(ctx context.Context, q OwnedAllocationHistoryQuery, de PaperDecisionEvaluator, oe ...PaperOutcomeEvaluator) (OwnedAllocationHistoryPage, error) {
	if q.ReceiverID != m.receiverID {
		return OwnedAllocationHistoryPage{}, ErrReceiverMismatch
	}
	if m.ownedAllocations.Checkpoint == nil && (len(m.ownedAllocationArchive) != 0 || len(m.ownedAllocationIndex.Members) != 0 || len(m.ownedAllocationIndex.Checkpoints) != 0 || len(m.ownedAllocationIndex.Batches) != 0 || len(m.ownedAllocationIndex.Financial) != 0) {
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
		if q.DecisionID != "" {
			if _, ok := m.ownedAllocationIndex.Members[1]; !ok || len(m.ownedAllocationIndex.Financial) != len(m.ownedAllocationIndex.Members) {
				return nil, ErrOwnedAllocationConflict
			}
			for sequence, ids := range m.ownedAllocationIndex.Financial {
				if err := ctx.Err(); err != nil {
					return nil, err
				}
				if sequence <= start || sequence > end || !slices.Contains(ids, q.DecisionID) {
					continue
				}
				member, ok := m.ownedAllocationIndex.Members[sequence]
				if !ok || member.Sequence != sequence {
					return nil, ErrOwnedAllocationConflict
				}
				index, _ := slices.BinarySearchFunc(rows, member, func(a, b ownedAllocationMember) int { return cmp.Compare(a.Sequence, b.Sequence) })
				if index <= q.Limit {
					rows = slices.Insert(rows, index, member)
					if len(rows) > q.Limit+1 {
						rows = rows[:q.Limit+1]
					}
				}
			}
			return rows, nil
		}
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
	out, _, _, err := p.readAllocationHistory(ctx, q, false, de, oe...)
	return out, err
}

// readAllocationHistory reads one sealed page. With active set it also
// replays the working journal in the same snapshot and returns that journal
// with its native catalogue; otherwise both are empty.
func (p *Postgres) readAllocationHistory(ctx context.Context, q OwnedAllocationHistoryQuery, active bool, de PaperDecisionEvaluator, oe ...PaperOutcomeEvaluator) (OwnedAllocationHistoryPage, ownedAllocationJournal, OwnedAllocationCatalog, error) {
	var selected allocationHistorySelection
	var out OwnedAllocationHistoryPage
	var snapshot pgx.Tx
	working := ownedAllocationJournal{Records: []retainedOwnedAllocation{}}
	catalog, _, err := p.ownedAllocationAccessFromJournal(ctx, false, nil, func(ctx context.Context, _ pgx.Tx, owner string, j ownedAllocationJournal, policies allocation.PolicyCatalog, decisions PaperDecisionCatalog, strategies paperstrategy.Catalog, retained map[string]retainedPaperDecision, x *ownedExecution, _ time.Time, declarations PaperEvaluationDeclarationCatalog, assessments PaperEvaluationAssessmentCatalog) (ownedAllocationJournal, bool, error) {
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
		var err error
		selected, err = postgresAllocationHistorySelection(ctx, tx, q, j.Checkpoint)
		if err == nil && active {
			working = ownedAllocationJournal{Checkpoint: j.Checkpoint, Records: j.Records}
			err = allocationTargetDependencies(working)
		}
		return working, err
	}, de, oe...)
	if err != nil {
		return OwnedAllocationHistoryPage{}, ownedAllocationJournal{}, OwnedAllocationCatalog{}, err
	}
	return out, working, catalog, nil
}

// postgresAllocationHistorySelection selects one sealed page in the caller's
// snapshot. current is that snapshot's working checkpoint.
func postgresAllocationHistorySelection(ctx context.Context, tx pgx.Tx, q OwnedAllocationHistoryQuery, current *OwnedAllocationCheckpoint) (allocationHistorySelection, error) {
	if current == nil {
		var exists bool
		if err := tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM mantik_owned_allocation_archive) OR EXISTS(SELECT 1 FROM mantik_owned_allocation_members) OR EXISTS(SELECT 1 FROM mantik_owned_allocation_checkpoints) OR EXISTS(SELECT 1 FROM mantik_owned_allocation_batches) OR EXISTS(SELECT 1 FROM mantik_owned_allocation_financial_refs)").Scan(&exists); err != nil {
			return allocationHistorySelection{}, err
		}
		if exists {
			return allocationHistorySelection{}, ErrOwnedAllocationConflict
		}
	}
	return selectAllocationHistory(ctx, q, current, func(n int) (OwnedAllocationCheckpoint, error) {
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
		query := "SELECT sequence,checkpoint_sequence,command_id,kind,record_sha256,item_sha256 FROM mantik_owned_allocation_members WHERE sequence>$1 AND sequence<=$2 ORDER BY sequence LIMIT $3"
		args := []any{start, end, q.Limit}
		if q.DecisionID != "" {
			var first bool
			if err := tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM mantik_owned_allocation_members WHERE sequence=1)").Scan(&first); err != nil {
				return nil, err
			}
			if !first {
				return nil, ErrOwnedAllocationConflict
			}
			query = allocationDecisionHistoryQuery
			args = []any{start, end, q.DecisionID, q.Limit + 1}
		}
		rows, err := tx.Query(ctx, query, args...)
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
}
