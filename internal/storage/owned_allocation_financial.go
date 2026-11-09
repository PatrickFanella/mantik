package storage

import (
	"context"
	"slices"

	"git.subcult.tv/subculture-collective/mantik/internal/allocation"
	"git.subcult.tv/subculture-collective/mantik/internal/paperaccount"
	"github.com/jackc/pgx/v5"
)

func allocationFinancialDecisions(item retainedOwnedAllocation) ([]string, error) {
	ids := map[string]bool{}
	for _, pin := range item.Record.Command.Decisions {
		ids[pin.ID] = true
	}
	effects := func(entries []allocation.LedgerEntry) {
		for _, e := range entries {
			if e.Command.Effect != nil {
				ids[e.Command.Effect.DecisionID] = true
			}
		}
	}
	effects(item.Record.Expirations)
	for _, advance := range item.Record.Executions {
		effects(advance.Entries)
	}
	if item.Execution != nil {
		for _, advance := range item.Execution.Executions {
			effects(advance.Entries)
		}
	}
	if len(ids) > MaxPaperDecisions {
		return nil, ErrOwnedAllocationCapacity
	}
	result := make([]string, 0, len(ids))
	for id := range ids {
		if !paperaccount.ID.MatchString(id) {
			return nil, ErrOwnedAllocationInvalid
		}
		result = append(result, id)
	}
	slices.Sort(result)
	return result, nil
}

func readAllocationFinancialRefs(ctx context.Context, tx pgx.Tx, end int, members []ownedAllocationMember) (map[int][]string, error) {
	rows, err := tx.Query(ctx, `SELECT f.sequence,f.decision_id
FROM mantik_owned_allocation_financial_refs f JOIN mantik_owned_allocation_members m USING(sequence)
WHERE m.checkpoint_sequence=$1 ORDER BY f.sequence,f.decision_id COLLATE "C" LIMIT $2`, end, MaxOwnedAllocations*MaxPaperDecisions+1)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := map[int][]string{}
	for _, m := range members {
		result[m.Sequence] = []string{}
	}
	count := 0
	for rows.Next() {
		var sequence int
		var id string
		if err := rows.Scan(&sequence, &id); err != nil {
			return nil, err
		}
		count++
		if _, ok := result[sequence]; !ok || count > MaxOwnedAllocations*MaxPaperDecisions {
			return nil, ErrOwnedAllocationInvalid
		}
		result[sequence] = append(result[sequence], id)
	}
	return result, rows.Err()
}
