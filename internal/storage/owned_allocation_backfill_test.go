package storage

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"git.subcult.tv/subculture-collective/mantik/internal/workerplan"
	"git.subcult.tv/subculture-collective/mantik/migrations"
)

// The native checkpoint contracts call this on actual owner records. Only
// derived indexes are removed in the disposable predecessor fixture.
func verifyAllocationIndexBackfill(t *testing.T, s ownedAllocationBackend, de PaperDecisionEvaluator, all OwnedAllocationCatalog) {
	t.Helper()
	ctx := t.Context()
	store := s.(OwnedAllocationIndexStore)
	command := OwnedAllocationIndexCommand{ReceiverID: all.ReceiverID}
	var verifyOriginals func()
	var verifyRestoredIndex func()
	var requireEmptyIndex func()
	if m, ok := s.(*Memory); ok {
		m.mu.Lock()
		originalIndex := clone(m.ownedAllocationIndex)
		working, archived := clone(m.ownedAllocations), clone(m.ownedAllocationArchive)
		m.ownedAllocationIndex = ownedAllocationArchiveIndex{}
		m.mu.Unlock()
		verifyOriginals = func() {
			t.Helper()
			m.mu.Lock()
			defer m.mu.Unlock()
			if !reflect.DeepEqual(working, m.ownedAllocations) || !reflect.DeepEqual(archived, m.ownedAllocationArchive) {
				t.Fatal("native backfill changed original records or the working journal")
			}
		}
		verifyRestoredIndex = func() {
			t.Helper()
			m.mu.Lock()
			defer m.mu.Unlock()
			if !reflect.DeepEqual(originalIndex, m.ownedAllocationIndex) {
				t.Fatal("native reconstruction changed original checkpoint or member metadata")
			}
		}
		requireEmptyIndex = func() {
			t.Helper()
			m.mu.Lock()
			defer m.mu.Unlock()
			if !reflect.DeepEqual(m.ownedAllocationIndex, ownedAllocationArchiveIndex{}) {
				t.Fatal("failed audit published partial index metadata")
			}
		}
		// A validly rehashed alias omission passes routine checkpoint replay,
		// but fails the final independent native checkpoint comparison after
		// the visitor has already built an earlier candidate segment.
		m.mu.Lock()
		damaged := clone(working)
		damaged.Checkpoint.Sources = []OwnedProposalSource{}
		damaged.Checkpoint.SHA256 = ""
		damaged.Checkpoint.SHA256 = workerplan.Hash(*damaged.Checkpoint)
		m.ownedAllocations = damaged
		m.mu.Unlock()
		if got, err := store.IndexOwnedAllocationHistory(ctx, command, de); err == nil || !reflect.DeepEqual(got, OwnedAllocationIndexReceipt{}) {
			t.Fatal("failed final native checkpoint audit acknowledged backfill", err)
		}
		requireEmptyIndex()
		m.mu.Lock()
		if !reflect.DeepEqual(m.ownedAllocations, damaged) {
			t.Fatal("backfill silently repaired a damaged working checkpoint")
		}
		m.ownedAllocations = working
		m.mu.Unlock()
	} else {
		p := s.(*Postgres)
		fingerprints := func(checkpoints bool) []string {
			t.Helper()
			query := `SELECT 'archive:'||sequence::text||':'||encode(sha256(payload),'hex') FROM mantik_owned_allocation_archive
UNION ALL SELECT 'journal:'||encode(sha256(payload),'hex') FROM mantik_owned_allocations ORDER BY 1`
			if checkpoints {
				query = "SELECT sequence::text||':'||encode(sha256(payload),'hex') FROM mantik_owned_allocation_checkpoints ORDER BY sequence"
			}
			rows, err := p.pool.Query(ctx, query)
			if err != nil {
				t.Fatal(err)
			}
			defer rows.Close()
			out := []string{}
			for rows.Next() {
				var sha string
				if err := rows.Scan(&sha); err != nil {
					t.Fatal(err)
				}
				out = append(out, sha)
			}
			if err := rows.Err(); err != nil {
				t.Fatal(err)
			}
			return out
		}
		originals, checkpoints := fingerprints(false), fingerprints(true)
		// Recreate the actual empty derived tables that a pre-109 journal
		// receives during upgrade. Schema and original owner rows stay intact.
		if _, err := p.pool.Exec(ctx, "DROP TABLE mantik_owned_allocation_financial_refs,mantik_owned_allocation_decision_refs,mantik_owned_allocation_members,mantik_owned_allocation_batches,mantik_owned_allocation_checkpoints"); err != nil {
			t.Fatal(err)
		}
		if _, err := p.pool.Exec(ctx, migrations.IndexedAllocationArchives); err != nil {
			t.Fatal(err)
		}
		if _, err := p.pool.Exec(ctx, migrations.AllocationFinancialReferences); err != nil {
			t.Fatal(err)
		}
		verifyOriginals = func() {
			t.Helper()
			if !reflect.DeepEqual(originals, fingerprints(false)) {
				t.Fatal("backfill rewrote original journal or archive bytes")
			}
		}
		verifyRestoredIndex = func() {
			t.Helper()
			if !reflect.DeepEqual(checkpoints, fingerprints(true)) {
				t.Fatal("native rebuilt checkpoint envelopes differ from recorded originals")
			}
		}
		requireEmptyIndex = func() {
			t.Helper()
			for _, table := range []string{"mantik_owned_allocation_checkpoints", "mantik_owned_allocation_batches", "mantik_owned_allocation_members", "mantik_owned_allocation_decision_refs", "mantik_owned_allocation_financial_refs"} {
				var count int
				if err := p.pool.QueryRow(ctx, "SELECT count(*) FROM "+table).Scan(&count); err != nil || count != 0 {
					t.Fatal("failed backfill left partial derived metadata", table, count, err)
				}
			}
		}
		if _, err := p.pool.Exec(ctx, `CREATE FUNCTION reject_native_index_backfill() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'native index backfill commit rollback fixture'; END $$;
CREATE CONSTRAINT TRIGGER reject_native_index_backfill AFTER INSERT ON mantik_owned_allocation_financial_refs DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION reject_native_index_backfill()`); err != nil {
			t.Fatal(err)
		}
		if got, err := store.IndexOwnedAllocationHistory(ctx, command, de); err == nil || !reflect.DeepEqual(got, OwnedAllocationIndexReceipt{}) {
			t.Fatal("failed COMMIT acknowledged native index backfill", err)
		}
		requireEmptyIndex()
		verifyOriginals()
		if _, err := p.pool.Exec(ctx, "DROP TRIGGER reject_native_index_backfill ON mantik_owned_allocation_financial_refs; DROP FUNCTION reject_native_index_backfill()"); err != nil {
			t.Fatal(err)
		}
	}
	requireEmptyIndex()
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if got, err := store.IndexOwnedAllocationHistory(canceled, command, de); !errors.Is(err, context.Canceled) || !reflect.DeepEqual(got, OwnedAllocationIndexReceipt{}) {
		t.Fatal("canceled native backfill acknowledged an index", err)
	}
	requireEmptyIndex()
	verifyOriginals()
	got, err := store.IndexOwnedAllocationHistory(ctx, command, de)
	if err != nil || got.Validate(command) != nil || got.ThroughSequence != 6 || got.MembersAdded != 6 || got.SegmentsAdded != 2 || got.CheckpointsAdded != 2 || got.AuditedRecords != len(all.Records) {
		t.Fatal("native legacy index reconstruction", got, err)
	}
	verifyOriginals()
	verifyRestoredIndex()
	again, err := store.IndexOwnedAllocationHistory(ctx, command, de)
	if err != nil || again.Validate(command) != nil || again.MembersAdded != 0 || again.SegmentsAdded != 0 || again.CheckpointsAdded != 0 || again.CheckpointSHA256 != got.CheckpointSHA256 {
		t.Fatal("native backfill retry changed its owner cut or rewrote indexes", err)
	}
	verifyOriginals()
	verifyRestoredIndex()
	// An intact later segment can coexist with an unindexed legacy prefix.
	// Keep both original checkpoint envelopes while removing only that prefix.
	if m, ok := s.(*Memory); ok {
		m.mu.Lock()
		delete(m.ownedAllocationIndex.Batches, 3)
		for n := 1; n <= 3; n++ {
			delete(m.ownedAllocationIndex.Members, n)
			delete(m.ownedAllocationIndex.Decisions, n)
			delete(m.ownedAllocationIndex.Financial, n)
		}
		m.mu.Unlock()
	} else {
		p := s.(*Postgres)
		if _, err := p.pool.Exec(ctx, `ALTER TABLE mantik_owned_allocation_financial_refs DISABLE TRIGGER USER;
ALTER TABLE mantik_owned_allocation_decision_refs DISABLE TRIGGER USER;
ALTER TABLE mantik_owned_allocation_members DISABLE TRIGGER USER;
ALTER TABLE mantik_owned_allocation_batches DISABLE TRIGGER USER;
DELETE FROM mantik_owned_allocation_financial_refs WHERE sequence<=3;
DELETE FROM mantik_owned_allocation_decision_refs WHERE sequence<=3;
DELETE FROM mantik_owned_allocation_members WHERE sequence<=3;
DELETE FROM mantik_owned_allocation_batches WHERE checkpoint_sequence=3;
ALTER TABLE mantik_owned_allocation_financial_refs ENABLE TRIGGER USER;
ALTER TABLE mantik_owned_allocation_decision_refs ENABLE TRIGGER USER;
ALTER TABLE mantik_owned_allocation_members ENABLE TRIGGER USER;
ALTER TABLE mantik_owned_allocation_batches ENABLE TRIGGER USER`); err != nil {
			t.Fatal(err)
		}
	}
	mixed, err := store.IndexOwnedAllocationHistory(ctx, command, de)
	if err != nil || mixed.Validate(command) != nil || mixed.SegmentsAdded != 1 || mixed.MembersAdded != 3 || mixed.CheckpointsAdded != 0 || mixed.CheckpointSHA256 != got.CheckpointSHA256 {
		t.Fatal("mixed legacy and original index reconstruction", mixed, err)
	}
	verifyOriginals()
	verifyRestoredIndex()
	verifyBackfillRefusesPartialIndex(t, s, command, de)
	if catalogue, err := s.OwnedAllocations(ctx, de); err != nil || !reflect.DeepEqual(catalogue, all) {
		t.Fatal("backfilled indexes changed complete original financial readback", err)
	}
	for _, record := range all.Records {
		if actual, err := s.(OwnedAllocationLookupStore).OwnedAllocationComparison(ctx, record.Command.ID, de); err != nil || !reflect.DeepEqual(actual, record) {
			t.Fatal("backfilled target changed a retained original", err)
		}
	}
	verifyAllocationHistoryPages(t, s, de, all)
}

func verifyBackfillRefusesPartialIndex(t *testing.T, s ownedAllocationBackend, command OwnedAllocationIndexCommand, de PaperDecisionEvaluator) {
	t.Helper()
	ctx := t.Context()
	if m, ok := s.(*Memory); ok {
		m.mu.Lock()
		original := clone(m.ownedAllocationIndex)
		m.ownedAllocationIndex.Financial[1] = []string{}
		damaged := clone(m.ownedAllocationIndex)
		m.mu.Unlock()
		defer func() {
			m.mu.Lock()
			defer m.mu.Unlock()
			if !reflect.DeepEqual(m.ownedAllocationIndex, damaged) {
				t.Error("backfill rewrote an incomplete existing index")
			}
			m.ownedAllocationIndex = original
		}()
	} else {
		p := s.(*Postgres)
		if _, err := p.pool.Exec(ctx, `ALTER TABLE mantik_owned_allocation_financial_refs DISABLE TRIGGER mantik_immutable_allocation_financial_refs;
DELETE FROM mantik_owned_allocation_financial_refs WHERE sequence=1;
ALTER TABLE mantik_owned_allocation_financial_refs ENABLE TRIGGER mantik_immutable_allocation_financial_refs`); err != nil {
			t.Fatal(err)
		}
		defer func() {
			var count int
			if err := p.pool.QueryRow(ctx, "SELECT count(*) FROM mantik_owned_allocation_financial_refs WHERE sequence=1").Scan(&count); err != nil || count != 0 {
				t.Error("backfill rewrote an incomplete existing index", err)
			}
			if _, err := p.pool.Exec(ctx, `INSERT INTO mantik_owned_allocation_financial_refs(sequence,decision_id)
SELECT a.sequence,d.decision_id FROM mantik_owned_allocation_archive a
CROSS JOIN LATERAL mantik_allocation_financial_decisions(convert_from(a.payload,'UTF8')::jsonb) d WHERE a.sequence=1`); err != nil {
				t.Error(err)
			}
		}()
	}
	if got, err := s.(OwnedAllocationIndexStore).IndexOwnedAllocationHistory(ctx, command, de); !errors.Is(err, ErrOwnedAllocationAudit) || !reflect.DeepEqual(got, OwnedAllocationIndexReceipt{}) {
		t.Fatal("backfill accepted an incomplete existing index", err)
	}
}
