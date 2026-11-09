package storage

import (
	"context"
	"maps"
	"slices"

	"github.com/jackc/pgx/v5"
)

// Allocation replay needs the original basis of each referenced decision,
// including proposals whose decision has left the active working catalogue.
// Resolve only journal/request dependencies after the complete active-owner
// check. These private dependencies do not enlarge the public active catalogue.
func allocationOwnerDecisions(ctx context.Context, active PaperDecisionCatalog, retained map[string]retainedPaperDecision, journal ownedAllocationJournal, pins []OwnedDecisionPin, original func(string) (retainedPaperDecision, error)) (PaperDecisionCatalog, map[string]retainedPaperDecision, error) {
	needed := map[string]string{}
	add := func(pins []OwnedDecisionPin) error {
		for _, p := range pins {
			if old, ok := needed[p.ID]; ok && old != p.SHA256 {
				return ErrOwnedAllocationConflict
			}
			needed[p.ID] = p.SHA256
		}
		return nil
	}
	if err := add(pins); err != nil {
		return PaperDecisionCatalog{}, nil, err
	}
	for _, records := range [][]retainedOwnedAllocation{journal.archived, journal.Records} {
		for _, item := range records {
			if err := ctx.Err(); err != nil {
				return PaperDecisionCatalog{}, nil, err
			}
			if err := add(item.Record.Command.Decisions); err != nil {
				return PaperDecisionCatalog{}, nil, err
			}
		}
	}
	out, owners := clone(active), maps.Clone(retained)
	if owners == nil {
		owners = map[string]retainedPaperDecision{}
	}
	for _, id := range slices.Sorted(maps.Keys(needed)) {
		if err := ctx.Err(); err != nil {
			return PaperDecisionCatalog{}, nil, err
		}
		d, ok := owners[id]
		if !ok {
			var err error
			d, err = original(id)
			if err != nil {
				return PaperDecisionCatalog{}, nil, err
			}
			owners[id] = d
			out.Records = append(out.Records, clone(d.Record))
		}
		if d.Record.Command.ID != id || d.Record.Command.ReceiverID != active.ReceiverID || d.Record.SHA256 != needed[id] {
			return PaperDecisionCatalog{}, nil, ErrOwnedAllocationConflict
		}
	}
	if err := ctx.Err(); err != nil {
		return PaperDecisionCatalog{}, nil, err
	}
	return out, owners, nil
}

func (m *Memory) allocationOwnerDecisionsLocked(ctx context.Context, active PaperDecisionCatalog, journal ownedAllocationJournal, pins []OwnedDecisionPin, eval PaperDecisionEvaluator) (PaperDecisionCatalog, map[string]retainedPaperDecision, error) {
	return allocationOwnerDecisions(ctx, active, m.paperDecisions, journal, pins, func(id string) (retainedPaperDecision, error) {
		r, retired, err := m.paperTargetMetadataLocked(ctx, "decision", id)
		if err != nil {
			return retainedPaperDecision{}, err
		}
		if !retired {
			return retainedPaperDecision{}, ErrOwnedAllocationConflict
		}
		owners, err := m.archivedPaperTargetLocked(ctx, r, eval, nil)
		if err != nil {
			return retainedPaperDecision{}, err
		}
		return owners.decisions[id], nil
	})
}

func readAllocationOwnerDecisions(ctx context.Context, tx pgx.Tx, active PaperDecisionCatalog, retained map[string]retainedPaperDecision, journal ownedAllocationJournal, pins []OwnedDecisionPin, eval PaperDecisionEvaluator) (PaperDecisionCatalog, map[string]retainedPaperDecision, error) {
	return allocationOwnerDecisions(ctx, active, retained, journal, pins, func(id string) (retainedPaperDecision, error) {
		r, retired, err := readPaperTargetMetadata(ctx, tx, "decision", id)
		if err != nil {
			return retainedPaperDecision{}, err
		}
		if !retired {
			return retainedPaperDecision{}, ErrOwnedAllocationConflict
		}
		owners, err := readArchivedPaperTarget(ctx, tx, r, eval, nil)
		if err != nil {
			return retainedPaperDecision{}, err
		}
		return owners.decisions[id], nil
	})
}
