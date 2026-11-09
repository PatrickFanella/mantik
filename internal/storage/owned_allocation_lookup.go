package storage

import (
	"context"
	"errors"
	"reflect"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/allocation"
	"git.subcult.tv/subculture-collective/mantik/internal/paperaccount"
	"git.subcult.tv/subculture-collective/mantik/internal/paperstrategy"
	"github.com/jackc/pgx/v5"
)

var ErrOwnedAllocationNotFound = errors.New("owned allocation comparison not found")

type OwnedAllocationLookupStore interface {
	OwnedAllocationComparison(context.Context, string, PaperDecisionEvaluator, ...PaperOutcomeEvaluator) (OwnedAllocationRecord, error)
}

func allocationComparisonFromCatalog(id string, catalog OwnedAllocationCatalog) (OwnedAllocationRecord, error) {
	for _, r := range catalog.Records {
		if r.Command.ID == id {
			return clone(r), nil
		}
	}
	return OwnedAllocationRecord{}, ErrOwnedAllocationNotFound
}

// Retirement must close allocation segments before moving their decision
// generation. A selected segment resolves at most one decision working set.
func allocationTargetDependencies(j ownedAllocationJournal) error {
	ids := map[string]bool{}
	for _, r := range j.Records {
		for _, pin := range r.Record.Command.Decisions {
			ids[pin.ID] = true
			if len(ids) > MaxPaperDecisions {
				return ErrOwnedAllocationCapacity
			}
		}
	}
	return nil
}

func activeAllocationTarget(id string, j ownedAllocationJournal) (bool, error) {
	for _, item := range j.Records {
		if item.Execution != nil && item.Execution.Command.ID == id {
			return false, ErrOwnedAllocationNotFound
		}
		if item.Record.Command.ID == id {
			return true, nil
		}
	}
	return false, nil
}

func selectedAllocationSegment(id string, segment ownedAllocationSegment) (ownedAllocationJournal, error) {
	j := ownedAllocationJournal{Checkpoint: segment.Before, Records: segment.Records}
	found, err := activeAllocationTarget(id, j)
	if err != nil {
		return ownedAllocationJournal{}, err
	}
	if !found {
		return ownedAllocationJournal{}, ErrOwnedAllocationConflict
	}
	if err := allocationTargetDependencies(j); err != nil {
		return ownedAllocationJournal{}, err
	}
	return j, nil
}

func (m *Memory) allocationTargetJournalLocked(ctx context.Context, id string) (ownedAllocationJournal, *ownedAllocationSegment, error) {
	j := m.ownedAllocations
	active, err := activeAllocationTarget(id, j)
	if err != nil {
		return ownedAllocationJournal{}, nil, err
	}
	if active {
		if j.Checkpoint != nil {
			segment, err := m.ownedAllocationSegmentLocked(ctx, j.Checkpoint.Sequence)
			if errors.Is(err, errOwnedAllocationIndexUnavailable) {
				return m.ownedAllocationHistoryLocked(), nil, nil
			}
			if err != nil {
				return ownedAllocationJournal{}, nil, err
			}
			if !reflect.DeepEqual(segment.After, *j.Checkpoint) {
				return ownedAllocationJournal{}, nil, ErrOwnedAllocationConflict
			}
		}
		return j, nil, allocationTargetDependencies(j)
	}
	indexed := 0
	for sequence, member := range m.ownedAllocationIndex.Members {
		if err := ctx.Err(); err != nil {
			return ownedAllocationJournal{}, nil, err
		}
		if member.CommandID == id {
			if indexed != 0 {
				return ownedAllocationJournal{}, nil, ErrOwnedAllocationConflict
			}
			indexed = sequence
		}
	}
	if indexed != 0 {
		segment, err := m.ownedAllocationSegmentLocked(ctx, indexed)
		if err != nil {
			return ownedAllocationJournal{}, nil, err
		}
		selected, err := selectedAllocationSegment(id, segment)
		return selected, &segment, err
	}
	for n, item := range m.ownedAllocationArchive {
		if err := ctx.Err(); err != nil {
			return ownedAllocationJournal{}, nil, err
		}
		if item.Execution != nil && item.Execution.Command.ID == id {
			return ownedAllocationJournal{}, nil, ErrOwnedAllocationNotFound
		}
		if item.Record.Command.ID != id {
			continue
		}
		segment, err := m.ownedAllocationSegmentLocked(ctx, n+1)
		if errors.Is(err, errOwnedAllocationIndexUnavailable) {
			return m.ownedAllocationHistoryLocked(), nil, nil
		}
		if err != nil {
			return ownedAllocationJournal{}, nil, err
		}
		selected, err := selectedAllocationSegment(id, segment)
		return selected, &segment, err
	}
	return ownedAllocationJournal{}, nil, ErrOwnedAllocationNotFound
}

func readAllocationTargetJournal(ctx context.Context, tx pgx.Tx, id string, j ownedAllocationJournal) (ownedAllocationJournal, *ownedAllocationSegment, error) {
	legacy := func() (ownedAllocationJournal, *ownedAllocationSegment, error) {
		var err error
		j.archived, err = readOwnedAllocationArchive(ctx, tx)
		return j, nil, err
	}
	active, err := activeAllocationTarget(id, j)
	if err != nil {
		return ownedAllocationJournal{}, nil, err
	}
	if active {
		if j.Checkpoint != nil {
			segment, err := readOwnedAllocationSegment(ctx, tx, j.Checkpoint.Sequence)
			if errors.Is(err, errOwnedAllocationIndexUnavailable) {
				return legacy()
			}
			if err != nil {
				return ownedAllocationJournal{}, nil, err
			}
			if !reflect.DeepEqual(segment.After, *j.Checkpoint) {
				return ownedAllocationJournal{}, nil, ErrOwnedAllocationConflict
			}
		}
		return j, nil, allocationTargetDependencies(j)
	}
	var sequence int
	err = tx.QueryRow(ctx, "SELECT sequence FROM mantik_owned_allocation_members WHERE command_id=$1", id).Scan(&sequence)
	if errors.Is(err, pgx.ErrNoRows) {
		err = tx.QueryRow(ctx, "SELECT sequence FROM mantik_owned_allocation_archive WHERE command_id=$1", id).Scan(&sequence)
	}
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			err = ErrOwnedAllocationNotFound
		}
		return ownedAllocationJournal{}, nil, err
	}
	segment, err := readOwnedAllocationSegment(ctx, tx, sequence)
	if errors.Is(err, errOwnedAllocationIndexUnavailable) {
		return legacy()
	}
	if err != nil {
		return ownedAllocationJournal{}, nil, err
	}
	selected, err := selectedAllocationSegment(id, segment)
	return selected, &segment, err
}

func (m *Memory) OwnedAllocationComparison(ctx context.Context, id string, de PaperDecisionEvaluator, outcomeEvals ...PaperOutcomeEvaluator) (OwnedAllocationRecord, error) {
	if !paperaccount.ID.MatchString(id) || de == nil {
		return OwnedAllocationRecord{}, ErrOwnedAllocationInvalid
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return OwnedAllocationRecord{}, err
	}
	j, segment, err := m.allocationTargetJournalLocked(ctx, id)
	if err != nil {
		return OwnedAllocationRecord{}, err
	}
	policies, decisions, strategies, err := m.ownedAllocationSourcesLocked(ctx, de)
	if err != nil {
		return OwnedAllocationRecord{}, err
	}
	declarations, err := paperEvaluationDeclarationCatalog(m.receiverID, m.paperEvaluationPlans, strategies, policies)
	if err != nil {
		return OwnedAllocationRecord{}, err
	}
	oe, err := allocationOutcomeEvaluator(outcomeEvals)
	if err != nil {
		return OwnedAllocationRecord{}, err
	}
	assessments, err := m.allocationAssessmentOwnersLocked(ctx, de, oe)
	if err != nil {
		return OwnedAllocationRecord{}, err
	}
	decisions, retained, err := m.allocationOwnerDecisionsLocked(ctx, decisions, j, nil, de)
	if err != nil {
		return OwnedAllocationRecord{}, err
	}
	var catalog OwnedAllocationCatalog
	if segment != nil {
		s := paperEvaluationSources{policies: policies, decisions: decisions, strategies: strategies, retained: retained, declarations: declarations}
		catalog, err = replayOwnedAllocationSegment(ctx, *segment, s, m.ownedExecutionLocked(), assessments)
	} else {
		catalog, err = replayOwnedAllocations(ctx, m.receiverID, j, policies, decisions, strategies, retained, m.ownedExecutionLocked(), declarations, assessments)
	}
	if err != nil {
		return OwnedAllocationRecord{}, err
	}
	if err := ctx.Err(); err != nil {
		return OwnedAllocationRecord{}, err
	}
	return allocationComparisonFromCatalog(id, catalog)
}

func (p *Postgres) OwnedAllocationComparison(ctx context.Context, id string, de PaperDecisionEvaluator, outcomeEvals ...PaperOutcomeEvaluator) (OwnedAllocationRecord, error) {
	if !paperaccount.ID.MatchString(id) || de == nil {
		return OwnedAllocationRecord{}, ErrOwnedAllocationInvalid
	}
	var segment *ownedAllocationSegment
	catalog, _, err := p.ownedAllocationAccessFromJournal(ctx, false, nil, func(ctx context.Context, _ string, j ownedAllocationJournal, policies allocation.PolicyCatalog, decisions PaperDecisionCatalog, strategies paperstrategy.Catalog, retained map[string]retainedPaperDecision, x *ownedExecution, _ time.Time, declarations PaperEvaluationDeclarationCatalog, assessments PaperEvaluationAssessmentCatalog) (ownedAllocationJournal, bool, error) {
		if segment != nil {
			s := paperEvaluationSources{policies: policies, decisions: decisions, strategies: strategies, retained: retained, declarations: declarations}
			_, err := replayOwnedAllocationSegment(ctx, *segment, s, x, assessments)
			if err != nil {
				return ownedAllocationJournal{}, false, err
			}
		}
		return j, false, nil
	}, func(ctx context.Context, tx pgx.Tx, j ownedAllocationJournal) (ownedAllocationJournal, error) {
		var err error
		j, segment, err = readAllocationTargetJournal(ctx, tx, id, j)
		return j, err
	}, de, outcomeEvals...)
	if err != nil {
		return OwnedAllocationRecord{}, err
	}
	return allocationComparisonFromCatalog(id, catalog)
}
