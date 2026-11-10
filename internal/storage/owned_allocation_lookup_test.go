package storage

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"

	"git.subcult.tv/subculture-collective/mantik/internal/workerplan"
)

// The shared checkpoint contract calls this with real original owner records
// for Memory and PostgreSQL, before and after approved fixture retirement.
func verifyAllocationTargetReads(t *testing.T, s ownedAllocationBackend, de PaperDecisionEvaluator, all OwnedAllocationCatalog) {
	t.Helper()
	ctx := t.Context()
	lookup := s.(OwnedAllocationLookupStore)
	for _, expected := range all.Records {
		got, err := lookup.OwnedAllocationComparison(ctx, expected.Command.ID, de)
		if err != nil || !reflect.DeepEqual(got, expected) {
			t.Fatal("targeted original or active comparison changed", expected.Command.ID, err)
		}
		got.Command.Decisions[0].ID = "caller-mutation"
		if again, err := lookup.OwnedAllocationComparison(ctx, expected.Command.ID, de); err != nil || !reflect.DeepEqual(again, expected) {
			t.Fatal("comparison lookup aliases the owner", err)
		}
	}
	if got, err := lookup.OwnedAllocationComparison(ctx, "absent", de); !errors.Is(err, ErrOwnedAllocationNotFound) || !reflect.DeepEqual(got, OwnedAllocationRecord{}) {
		t.Fatal("missing comparison returned a record", err)
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if got, err := lookup.OwnedAllocationComparison(canceled, all.Records[0].Command.ID, de); !errors.Is(err, context.Canceled) || !reflect.DeepEqual(got, OwnedAllocationRecord{}) {
		t.Fatal("cancellation returned a partial comparison", err)
	}
	if _, err := lookup.OwnedAllocationComparison(ctx, "../operations", de); !errors.Is(err, ErrOwnedAllocationInvalid) {
		t.Fatal("invalid target accepted", err)
	}
	if m, ok := s.(*Memory); ok {
		t.Run("target-legacy-history", func(t *testing.T) {
			m.mu.Lock()
			index := m.ownedAllocationIndex
			m.ownedAllocationIndex = ownedAllocationArchiveIndex{}
			m.mu.Unlock()
			defer func() { m.mu.Lock(); m.ownedAllocationIndex = index; m.mu.Unlock() }()
			if got, err := m.OwnedAllocationHistory(t.Context(), OwnedAllocationHistoryQuery{ReceiverID: all.ReceiverID, Limit: 2}, de); err == nil || !reflect.DeepEqual(got, OwnedAllocationHistoryPage{}) {
				t.Fatal("unindexed legacy history became a successful partial page")
			}
			for _, n := range []int{0, len(all.Records) - 1} {
				if got, err := lookup.OwnedAllocationComparison(t.Context(), all.Records[n].Command.ID, de); err != nil || !reflect.DeepEqual(got, all.Records[n]) {
					t.Fatal("legacy checkpoint lost original target", err)
				}
			}
		})
		t.Run("target-missing-original-payload", func(t *testing.T) {
			m.mu.Lock()
			original := m.ownedAllocationArchive[0]
			m.ownedAllocationArchive[0] = retainedOwnedAllocation{}
			m.mu.Unlock()
			defer func() { m.mu.Lock(); m.ownedAllocationArchive[0] = original; m.mu.Unlock() }()
			if got, err := lookup.OwnedAllocationComparison(t.Context(), all.Records[0].Command.ID, de); err == nil || errors.Is(err, ErrOwnedAllocationNotFound) || !reflect.DeepEqual(got, OwnedAllocationRecord{}) {
				t.Fatal("missing indexed original became ordinary absence", err)
			}
		})
		t.Run("target-native-end-checkpoint", func(t *testing.T) {
			m.mu.Lock()
			original := clone(m.ownedAllocationIndex.Checkpoints[3])
			forged := clone(original)
			forged.Ledgers[0].Base.Snapshot.RealizedPnLCents++
			forged.Ledgers[0].SHA256 = ""
			forged.Ledgers[0].SHA256 = workerplan.Hash(forged.Ledgers[0])
			forged.SHA256 = ""
			forged.SHA256 = workerplan.Hash(forged)
			m.ownedAllocationIndex.Checkpoints[3] = forged
			m.mu.Unlock()
			defer func() { m.mu.Lock(); m.ownedAllocationIndex.Checkpoints[3] = original; m.mu.Unlock() }()
			if got, err := lookup.OwnedAllocationComparison(t.Context(), all.Records[0].Command.ID, de); !errors.Is(err, ErrOwnedAllocationAudit) || !reflect.DeepEqual(got, OwnedAllocationRecord{}) {
				t.Fatal("self-rehashed original checkpoint replaced native replay", err)
			}
		})
	}

	// Deliberately damage an unrelated later archived record. The first segment
	// remains readable, whereas a complete-history read must still reject it.
	first, later := all.Records[0], all.Records[3]
	if m, ok := s.(*Memory); ok {
		m.mu.Lock()
		original := clone(m.ownedAllocationArchive[3])
		m.ownedAllocationArchive[3].Record.At = m.ownedAllocationArchive[3].Record.At.Add(1)
		m.mu.Unlock()
		defer func() { m.mu.Lock(); m.ownedAllocationArchive[3] = original; m.mu.Unlock() }()
	} else {
		p := s.(*Postgres)
		var raw []byte
		if err := p.pool.QueryRow(ctx, "SELECT payload FROM mantik_owned_allocation_archive WHERE sequence=4").Scan(&raw); err != nil {
			t.Fatal(err)
		}
		var item retainedOwnedAllocation
		if err := json.Unmarshal(raw, &item); err != nil {
			t.Fatal(err)
		}
		item.Record.At = item.Record.At.Add(1)
		damaged, _ := json.Marshal(item)
		if _, err := p.pool.Exec(ctx, "UPDATE mantik_owned_allocation_archive SET payload=$1 WHERE sequence=4", damaged); err != nil {
			t.Fatal("fixture archive corruption", err)
		}
		defer func() {
			if _, err := p.pool.Exec(ctx, "UPDATE mantik_owned_allocation_archive SET payload=$1 WHERE sequence=4", raw); err != nil {
				t.Fatal(err)
			}
		}()
	}
	if got, err := lookup.OwnedAllocationComparison(ctx, first.Command.ID, de); err != nil || !reflect.DeepEqual(got, first) {
		t.Fatal("targeted read loaded an unrelated later segment", err)
	}
	if got, err := lookup.OwnedAllocationComparison(ctx, later.Command.ID, de); err == nil || !reflect.DeepEqual(got, OwnedAllocationRecord{}) {
		t.Fatal("damaged selected segment returned a record", err)
	}
	pages := s.(OwnedAllocationHistoryStore)
	firstPage, err := pages.OwnedAllocationHistory(ctx, OwnedAllocationHistoryQuery{ReceiverID: all.ReceiverID, Limit: 2}, de)
	if err != nil || firstPage.NextAfter == "" {
		t.Fatal("intact original page loaded an unrelated damaged segment", err)
	}
	if got, err := pages.OwnedAllocationHistory(ctx, OwnedAllocationHistoryQuery{ReceiverID: all.ReceiverID, Limit: 2, After: firstPage.NextAfter}, de); err == nil || !reflect.DeepEqual(got, OwnedAllocationHistoryPage{}) {
		t.Fatal("damaged allocation history returned a partial page", err)
	}
	if got, err := s.OwnedAllocations(ctx, de); err == nil || !reflect.DeepEqual(got, OwnedAllocationCatalog{}) {
		t.Fatal("partial target proof replaced complete history validation", err)
	}
}
