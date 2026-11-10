package storage

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"

	"git.subcult.tv/subculture-collective/mantik/internal/workerplan"
	"github.com/jackc/pgx/v5"
)

// Index metadata pins complete original retained items, including captured
// policies/declarations. It is custody, not a substitute for native replay.
type ownedAllocationMember struct {
	Sequence           int
	CheckpointSequence int
	CommandID          string
	Kind               string
	RecordSHA256       string
	ItemSHA256         string
}

type ownedAllocationBatch struct {
	First         int
	MembersSHA256 string
}

type ownedAllocationArchiveIndex struct {
	Checkpoints map[int]OwnedAllocationCheckpoint
	Batches     map[int]ownedAllocationBatch
	Members     map[int]ownedAllocationMember
	Decisions   map[int][]OwnedDecisionPin
	Financial   map[int][]string
}

var errOwnedAllocationIndexUnavailable = errors.New("allocation segment has no original index")

type ownedAllocationSegment struct {
	Before  *OwnedAllocationCheckpoint
	After   OwnedAllocationCheckpoint
	Records []retainedOwnedAllocation
}

func allocationArchiveMembers(journal ownedAllocationJournal, previous *OwnedAllocationCheckpoint) ([]ownedAllocationMember, error) {
	if len(journal.sealing) == 0 {
		return nil, nil
	}
	c := journal.Checkpoint
	first := 1
	chain := ""
	if previous != nil {
		unsigned := *previous
		unsigned.SHA256 = ""
		if workerplan.Hash(unsigned) != previous.SHA256 {
			return nil, ErrOwnedAllocationInvalid
		}
		first, chain = previous.Sequence+1, previous.SealedSHA256
	}
	if c == nil || len(journal.sealing) > MaxOwnedAllocations || c.Sequence != first+len(journal.sealing)-1 {
		return nil, ErrOwnedAllocationInvalid
	}
	unsigned := *c
	unsigned.SHA256 = ""
	if workerplan.Hash(unsigned) != c.SHA256 {
		return nil, ErrOwnedAllocationInvalid
	}
	members := make([]ownedAllocationMember, 0, len(journal.sealing))
	for i, item := range journal.sealing {
		m := ownedAllocationMember{Sequence: first + i, CheckpointSequence: c.Sequence, CommandID: item.Record.Command.ID, Kind: "comparison", RecordSHA256: item.Record.SHA256, ItemSHA256: workerplan.Hash(item)}
		if item.Execution != nil {
			m.CommandID, m.Kind, m.RecordSHA256 = item.Execution.Command.ID, "execution", item.Execution.SHA256
		}
		if m.CommandID == "" || !decisionPin.MatchString(m.RecordSHA256) {
			return nil, ErrOwnedAllocationInvalid
		}
		chain = sealedChain(chain, m.Sequence, item)
		members = append(members, m)
	}
	if chain != c.SealedSHA256 {
		return nil, ErrOwnedAllocationInvalid
	}
	return members, nil
}

func (m *Memory) retainOwnedAllocationIndexLocked(next ownedAllocationJournal) error {
	return retainOwnedAllocationIndex(&m.ownedAllocationIndex, m.ownedAllocations.Checkpoint, next)
}

func retainOwnedAllocationIndex(index *ownedAllocationArchiveIndex, previous *OwnedAllocationCheckpoint, next ownedAllocationJournal) error {
	members, err := allocationArchiveMembers(next, previous)
	if err != nil || len(members) == 0 {
		return err
	}
	if (index.Checkpoints == nil) != (index.Batches == nil) || (index.Checkpoints == nil) != (index.Members == nil) || (index.Checkpoints == nil) != (index.Decisions == nil) || (index.Checkpoints == nil) != (index.Financial == nil) {
		return ErrOwnedAllocationInvalid
	}
	if _, exists := index.Batches[next.Checkpoint.Sequence]; exists {
		return ErrOwnedAllocationConflict
	}
	for _, c := range []*OwnedAllocationCheckpoint{previous, next.Checkpoint} {
		if c != nil {
			if old, ok := index.Checkpoints[c.Sequence]; ok && !reflect.DeepEqual(old, *c) {
				return ErrOwnedAllocationConflict
			}
		}
	}
	financial := map[int][]string{}
	for i, member := range members {
		if _, exists := index.Members[member.Sequence]; exists {
			return ErrOwnedAllocationConflict
		}
		if _, exists := index.Decisions[member.Sequence]; exists {
			return ErrOwnedAllocationConflict
		}
		if _, exists := index.Financial[member.Sequence]; exists {
			return ErrOwnedAllocationConflict
		}
		ids, err := allocationFinancialDecisions(next.sealing[i])
		if err != nil {
			return err
		}
		financial[member.Sequence] = ids
	}
	if index.Checkpoints == nil {
		*index = ownedAllocationArchiveIndex{Checkpoints: map[int]OwnedAllocationCheckpoint{}, Batches: map[int]ownedAllocationBatch{}, Members: map[int]ownedAllocationMember{}, Decisions: map[int][]OwnedDecisionPin{}, Financial: map[int][]string{}}
	}
	for _, c := range []*OwnedAllocationCheckpoint{previous, next.Checkpoint} {
		if c != nil {
			index.Checkpoints[c.Sequence] = clone(*c)
		}
	}
	index.Batches[next.Checkpoint.Sequence] = ownedAllocationBatch{members[0].Sequence, workerplan.Hash(members)}
	for i, member := range members {
		index.Members[member.Sequence] = member
		index.Decisions[member.Sequence] = clone(next.sealing[i].Record.Command.Decisions)
		index.Financial[member.Sequence] = financial[member.Sequence]
	}
	return nil
}

// A segment is only an original custody view. Its consumer must re-execute it
// with native owner inputs and compare the rebuilt checkpoint with After.
func finishOwnedAllocationSegment(sequence int, batch ownedAllocationBatch, segment ownedAllocationSegment, members []ownedAllocationMember, pins map[int][]OwnedDecisionPin, financial map[int][]string) (ownedAllocationSegment, error) {
	if sequence < batch.First || sequence > segment.After.Sequence || len(segment.Records) == 0 || len(segment.Records) > MaxOwnedAllocations || len(members) != len(segment.Records) || len(pins) != len(members) || len(financial) != len(members) {
		return ownedAllocationSegment{}, ErrOwnedAllocationInvalid
	}
	expected, err := allocationArchiveMembers(ownedAllocationJournal{Checkpoint: &segment.After, sealing: segment.Records}, segment.Before)
	if err != nil || expected[0].Sequence != batch.First || !reflect.DeepEqual(expected, members) || workerplan.Hash(members) != batch.MembersSHA256 {
		return ownedAllocationSegment{}, ErrOwnedAllocationInvalid
	}
	for i, item := range segment.Records {
		if !reflect.DeepEqual(item.Record.Command.Decisions, pins[members[i].Sequence]) {
			return ownedAllocationSegment{}, ErrOwnedAllocationInvalid
		}
		ids, err := allocationFinancialDecisions(item)
		if err != nil || !reflect.DeepEqual(ids, financial[members[i].Sequence]) {
			return ownedAllocationSegment{}, ErrOwnedAllocationInvalid
		}
	}
	return clone(segment), nil
}

func replayOwnedAllocationSegment(ctx context.Context, segment ownedAllocationSegment, sources paperEvaluationSources, execution *ownedExecution, assessments ...PaperEvaluationAssessmentCatalog) (OwnedAllocationCatalog, error) {
	journal := ownedAllocationJournal{Checkpoint: segment.Before, Records: segment.Records}
	catalog, state, err := replayOwnedJournal(ctx, sources.declarations.ReceiverID, journal, sources.policies, sources.decisions, sources.strategies, sources.retained, execution, sources.declarations, assessments...)
	if err != nil {
		return OwnedAllocationCatalog{}, err
	}
	rebuilt, err := buildOwnedCheckpoint(ctx, segment.Before, segment.Records, catalog, state)
	if err != nil {
		return OwnedAllocationCatalog{}, err
	}
	if err := ctx.Err(); err != nil {
		return OwnedAllocationCatalog{}, err
	}
	if !reflect.DeepEqual(rebuilt, segment.After) {
		return OwnedAllocationCatalog{}, ErrOwnedAllocationAudit
	}
	return catalog, nil
}

func (m *Memory) ownedAllocationSegmentLocked(ctx context.Context, sequence int) (ownedAllocationSegment, error) {
	if err := ctx.Err(); err != nil {
		return ownedAllocationSegment{}, err
	}
	index := m.ownedAllocationIndex
	member, ok := index.Members[sequence]
	if !ok {
		for end, batch := range index.Batches {
			if err := ctx.Err(); err != nil {
				return ownedAllocationSegment{}, err
			}
			if sequence >= batch.First && sequence <= end {
				return ownedAllocationSegment{}, ErrOwnedAllocationInvalid
			}
		}
		return ownedAllocationSegment{}, errOwnedAllocationIndexUnavailable
	}
	batch, ok := index.Batches[member.CheckpointSequence]
	if !ok || batch.First < 1 || member.CheckpointSequence-batch.First+1 > MaxOwnedAllocations {
		return ownedAllocationSegment{}, ErrOwnedAllocationInvalid
	}
	end, ok := index.Checkpoints[member.CheckpointSequence]
	if !ok || end.Sequence > len(m.ownedAllocationArchive) || end.Sequence < batch.First {
		return ownedAllocationSegment{}, ErrOwnedAllocationInvalid
	}
	segment := ownedAllocationSegment{After: end, Records: m.ownedAllocationArchive[batch.First-1 : end.Sequence]}
	if batch.First > 1 {
		before, ok := index.Checkpoints[batch.First-1]
		if !ok {
			return ownedAllocationSegment{}, ErrOwnedAllocationInvalid
		}
		segment.Before = &before
	}
	members, pins, financial := []ownedAllocationMember{}, map[int][]OwnedDecisionPin{}, map[int][]string{}
	for n := batch.First; n <= end.Sequence; n++ {
		if err := ctx.Err(); err != nil {
			return ownedAllocationSegment{}, err
		}
		member, ok := index.Members[n]
		if !ok {
			return ownedAllocationSegment{}, ErrOwnedAllocationInvalid
		}
		members = append(members, member)
		pins[n] = index.Decisions[n]
		financial[n] = index.Financial[n]
	}
	return finishOwnedAllocationSegment(sequence, batch, segment, members, pins, financial)
}

func readOwnedAllocationSegment(ctx context.Context, tx pgx.Tx, sequence int) (ownedAllocationSegment, error) {
	var end int
	var batch ownedAllocationBatch
	err := tx.QueryRow(ctx, `SELECT m.checkpoint_sequence,b.first_sequence,b.members_sha256 FROM mantik_owned_allocation_members m LEFT JOIN mantik_owned_allocation_batches b USING(checkpoint_sequence) WHERE m.sequence=$1`, sequence).Scan(&end, &batch.First, &batch.MembersSHA256)
	if errors.Is(err, pgx.ErrNoRows) {
		var reserved bool
		if err := tx.QueryRow(ctx, `SELECT coalesce((SELECT checkpoint_sequence>=$1 FROM mantik_owned_allocation_batches WHERE first_sequence<=$1 ORDER BY first_sequence DESC LIMIT 1),false)`, sequence).Scan(&reserved); err != nil {
			return ownedAllocationSegment{}, err
		}
		if reserved {
			return ownedAllocationSegment{}, ErrOwnedAllocationInvalid
		}
		return ownedAllocationSegment{}, errOwnedAllocationIndexUnavailable
	}
	if err != nil {
		return ownedAllocationSegment{}, err
	}
	if batch.First < 1 || end < batch.First || end-batch.First+1 > MaxOwnedAllocations {
		return ownedAllocationSegment{}, ErrOwnedAllocationInvalid
	}
	readCheckpoint := func(n int) (OwnedAllocationCheckpoint, error) {
		var raw []byte
		var sha string
		if err := tx.QueryRow(ctx, "SELECT payload,sha256 FROM mantik_owned_allocation_checkpoints WHERE sequence=$1", n).Scan(&raw, &sha); err != nil {
			return OwnedAllocationCheckpoint{}, err
		}
		var c OwnedAllocationCheckpoint
		if workerplan.ReadBoundedShape(raw, &c, MaxOwnedAllocationBytes) != nil || c.Sequence != n || c.SHA256 != sha {
			return OwnedAllocationCheckpoint{}, ErrOwnedAllocationInvalid
		}
		return c, nil
	}
	c, err := readCheckpoint(end)
	if err != nil {
		return ownedAllocationSegment{}, err
	}
	segment := ownedAllocationSegment{After: c, Records: []retainedOwnedAllocation{}}
	if batch.First > 1 {
		before, err := readCheckpoint(batch.First - 1)
		if err != nil {
			return ownedAllocationSegment{}, err
		}
		segment.Before = &before
	}
	rows, err := tx.Query(ctx, `SELECT m.sequence,m.checkpoint_sequence,m.command_id,m.kind,m.record_sha256,m.item_sha256,a.payload FROM mantik_owned_allocation_members m LEFT JOIN mantik_owned_allocation_archive a USING(sequence) WHERE m.checkpoint_sequence=$1 ORDER BY m.sequence LIMIT $2`, end, MaxOwnedAllocations+1)
	if err != nil {
		return ownedAllocationSegment{}, err
	}
	members := []ownedAllocationMember{}
	bytes := 0
	for rows.Next() {
		var member ownedAllocationMember
		var raw []byte
		if err := rows.Scan(&member.Sequence, &member.CheckpointSequence, &member.CommandID, &member.Kind, &member.RecordSHA256, &member.ItemSHA256, &raw); err != nil {
			rows.Close()
			return ownedAllocationSegment{}, err
		}
		bytes += len(raw)
		var item retainedOwnedAllocation
		if bytes > MaxOwnedAllocationBytes || len(members) >= MaxOwnedAllocations || workerplan.ReadBoundedShape(raw, &item, MaxOwnedAllocationBytes) != nil || workerplan.Digest(raw) != member.ItemSHA256 {
			rows.Close()
			return ownedAllocationSegment{}, ErrOwnedAllocationInvalid
		}
		members, segment.Records = append(members, member), append(segment.Records, item)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return ownedAllocationSegment{}, err
	}
	rows, err = tx.Query(ctx, `SELECT d.sequence,d.decision_id,d.decision_sha256 FROM mantik_owned_allocation_decision_refs d JOIN mantik_owned_allocation_members m USING(sequence) WHERE m.checkpoint_sequence=$1 ORDER BY d.sequence,d.decision_id COLLATE "C" LIMIT $2`, end, MaxOwnedAllocations*MaxPaperDecisions+1)
	if err != nil {
		return ownedAllocationSegment{}, err
	}
	defer rows.Close()
	pins := map[int][]OwnedDecisionPin{}
	for _, member := range members {
		pins[member.Sequence] = nil
	}
	count := 0
	for rows.Next() {
		var n int
		var pin OwnedDecisionPin
		if err := rows.Scan(&n, &pin.ID, &pin.SHA256); err != nil {
			return ownedAllocationSegment{}, err
		}
		count++
		if _, ok := pins[n]; !ok || count > MaxOwnedAllocations*MaxPaperDecisions {
			return ownedAllocationSegment{}, ErrOwnedAllocationInvalid
		}
		pins[n] = append(pins[n], pin)
	}
	if err := rows.Err(); err != nil {
		return ownedAllocationSegment{}, err
	}
	rows.Close()
	financial, err := readAllocationFinancialRefs(ctx, tx, end, members)
	if err != nil {
		return ownedAllocationSegment{}, err
	}
	if err := ctx.Err(); err != nil {
		return ownedAllocationSegment{}, err
	}
	return finishOwnedAllocationSegment(sequence, batch, segment, members, pins, financial)
}

func writeOwnedAllocationIndex(ctx context.Context, tx pgx.Tx, next ownedAllocationJournal, previous *OwnedAllocationCheckpoint) error {
	members, err := allocationArchiveMembers(next, previous)
	if err != nil || len(members) == 0 {
		return err
	}
	for _, c := range []*OwnedAllocationCheckpoint{previous, next.Checkpoint} {
		if c == nil {
			continue
		}
		raw, err := json.Marshal(c)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO mantik_owned_allocation_checkpoints(sequence,sha256,payload) VALUES($1,$2,$3) ON CONFLICT(sequence) DO NOTHING`, c.Sequence, c.SHA256, raw); err != nil {
			return err
		}
		var stored []byte
		if err := tx.QueryRow(ctx, "SELECT payload FROM mantik_owned_allocation_checkpoints WHERE sequence=$1 AND sha256=$2", c.Sequence, c.SHA256).Scan(&stored); err != nil {
			return err
		}
		var original OwnedAllocationCheckpoint
		if workerplan.ReadBoundedShape(stored, &original, MaxOwnedAllocationBytes) != nil || !reflect.DeepEqual(original, *c) {
			return ErrOwnedAllocationConflict
		}
	}
	if _, err := tx.Exec(ctx, "INSERT INTO mantik_owned_allocation_batches(checkpoint_sequence,first_sequence,members_sha256) VALUES($1,$2,$3)", next.Checkpoint.Sequence, members[0].Sequence, workerplan.Hash(members)); err != nil {
		return err
	}
	for i, member := range members {
		if _, err := tx.Exec(ctx, `INSERT INTO mantik_owned_allocation_members(sequence,checkpoint_sequence,command_id,kind,record_sha256,item_sha256) VALUES($1,$2,$3,$4,$5,$6)`, member.Sequence, member.CheckpointSequence, member.CommandID, member.Kind, member.RecordSHA256, member.ItemSHA256); err != nil {
			return err
		}
		for _, pin := range next.sealing[i].Record.Command.Decisions {
			if _, err := tx.Exec(ctx, "INSERT INTO mantik_owned_allocation_decision_refs(sequence,decision_id,decision_sha256) VALUES($1,$2,$3)", member.Sequence, pin.ID, pin.SHA256); err != nil {
				return err
			}
		}
		ids, err := allocationFinancialDecisions(next.sealing[i])
		if err != nil {
			return err
		}
		for _, id := range ids {
			if _, err := tx.Exec(ctx, "INSERT INTO mantik_owned_allocation_financial_refs(sequence,decision_id) VALUES($1,$2)", member.Sequence, id); err != nil {
				return err
			}
		}
	}
	return nil
}
