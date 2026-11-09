package storage

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"git.subcult.tv/subculture-collective/mantik/internal/workerplan"
)

func TestPostgresContractsAllocationIndexUpgrade(t *testing.T) {
	p := timerPostgres(t, "allocationindexupgrade")
	ctx := t.Context()
	if _, err := p.pool.Exec(ctx, `DROP INDEX mantik_paper_history_page;
DROP TABLE mantik_owned_allocation_financial_refs,mantik_owned_allocation_decision_refs,mantik_owned_allocation_members,mantik_owned_allocation_batches,mantik_owned_allocation_checkpoints;
DELETE FROM mantik_schema WHERE version>=109;
CREATE FUNCTION reject_allocation_index_migration() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.version=109 THEN RAISE EXCEPTION 'allocation index migration rollback fixture'; END IF; RETURN NEW; END $$;
CREATE TRIGGER reject_allocation_index_migration BEFORE INSERT ON mantik_schema FOR EACH ROW EXECUTE FUNCTION reject_allocation_index_migration()`); err != nil {
		t.Fatal(err)
	}
	if err := p.migrate(ctx); err == nil {
		t.Fatal("failed allocation index migration committed")
	}
	for _, table := range []string{"mantik_owned_allocation_checkpoints", "mantik_owned_allocation_batches", "mantik_owned_allocation_members", "mantik_owned_allocation_decision_refs", "mantik_owned_allocation_financial_refs"} {
		var exists bool
		if err := p.pool.QueryRow(ctx, "SELECT to_regclass($1) IS NOT NULL", table).Scan(&exists); err != nil || exists {
			t.Fatal("failed migration left partial archive index", table, err)
		}
	}
	if _, err := p.pool.Exec(ctx, "DROP TRIGGER reject_allocation_index_migration ON mantik_schema; DROP FUNCTION reject_allocation_index_migration()"); err != nil {
		t.Fatal(err)
	}
	if err := p.migrate(ctx); err != nil {
		t.Fatal(err)
	}
	var sha string
	if err := p.pool.QueryRow(ctx, "SELECT sha256 FROM mantik_schema WHERE version=109").Scan(&sha); err != nil {
		t.Fatal(err)
	}
	if _, err := p.pool.Exec(ctx, "UPDATE mantik_schema SET sha256='damaged' WHERE version=109"); err != nil {
		t.Fatal(err)
	}
	if err := p.migrate(ctx); err == nil {
		t.Fatal("index migration checksum change accepted")
	}
	if _, err := p.pool.Exec(ctx, "UPDATE mantik_schema SET sha256=$1 WHERE version=109", sha); err != nil {
		t.Fatal(err)
	}
	if err := p.migrate(ctx); err != nil {
		t.Fatal(err)
	}
}

// The original segment must be enough to re-execute its records from its
// original checkpoint, independently of every later sealed segment.
func verifyAllocationIndexSegments(t *testing.T, s ownedAllocationBackend, eval PaperDecisionEvaluator) {
	t.Helper()
	ctx := t.Context()
	verify := func(sources paperEvaluationSources, read func(int) (ownedAllocationSegment, error), execution *ownedExecution) {
		for _, sequence := range []int{1, 4} {
			segment, err := read(sequence)
			if err != nil || len(segment.Records) != 3 || segment.After.Sequence != sequence+2 || (segment.Before == nil) != (sequence == 1) {
				t.Fatal("original segment", sequence, err)
			}
			catalog, err := replayOwnedAllocationSegment(ctx, segment, sources, execution)
			if err != nil || len(catalog.Records) != len(segment.Records) {
				t.Fatal("segment native replay", err)
			}
			forged := clone(segment)
			forged.After.Ledgers[0].Base.Snapshot.RealizedPnLCents++
			forged.After.Ledgers[0].SHA256 = ""
			forged.After.Ledgers[0].SHA256 = workerplan.Hash(forged.After.Ledgers[0])
			forged.After.SHA256 = ""
			forged.After.SHA256 = workerplan.Hash(forged.After)
			if got, err := replayOwnedAllocationSegment(ctx, forged, sources, execution); !errors.Is(err, ErrOwnedAllocationAudit) || !reflect.DeepEqual(got, OwnedAllocationCatalog{}) {
				t.Fatal("segment native replay accepted changed end checkpoint", sequence, err)
			}
			segment.Records[0].Record.At = segment.Records[0].Record.At.Add(1)
			again, err := read(sequence)
			if err != nil || reflect.DeepEqual(again, segment) {
				t.Fatal("segment read aliased owner custody", err)
			}
		}
		if got, err := read(8); !errors.Is(err, errOwnedAllocationIndexUnavailable) || !reflect.DeepEqual(got, ownedAllocationSegment{}) {
			t.Fatal("active/unindexed record invented a sealed segment", err)
		}
	}
	if m, ok := s.(*Memory); ok {
		m.mu.Lock()
		defer m.mu.Unlock()
		sources, err := m.paperEvaluationContextLocked()
		if err != nil {
			t.Fatal(err)
		}
		sources.decisions, err = m.activePaperDecisionsLocked(ctx, sources.strategies, sources.feed, eval)
		if err != nil {
			t.Fatal(err)
		}
		read := func(n int) (ownedAllocationSegment, error) { return m.ownedAllocationSegmentLocked(ctx, n) }
		verify(sources, read, m.ownedExecutionLocked())
		before := m.ownedAllocationIndex.Checkpoints[3]
		delete(m.ownedAllocationIndex.Checkpoints, 3)
		if got, err := read(4); err == nil || !reflect.DeepEqual(got, ownedAllocationSegment{}) {
			t.Fatal("missing predecessor checkpoint accepted", err)
		}
		m.ownedAllocationIndex.Checkpoints[3] = before
		member := m.ownedAllocationIndex.Members[2]
		delete(m.ownedAllocationIndex.Members, 2)
		if got, err := read(1); err == nil || !reflect.DeepEqual(got, ownedAllocationSegment{}) {
			t.Fatal("missing sealed member returned partial segment", err)
		}
		if got, err := read(2); !errors.Is(err, ErrOwnedAllocationInvalid) || !reflect.DeepEqual(got, ownedAllocationSegment{}) {
			t.Fatal("missing reserved member was labelled legacy/unindexed", err)
		}
		m.ownedAllocationIndex.Members[2] = member
		return
	}
	p := s.(*Postgres)
	tx, err := p.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(context.Background())
	sources, err := readPaperEvaluationContext(ctx, tx, false, p.portfolioEvaluator)
	if err != nil {
		t.Fatal(err)
	}
	sources.decisions, err = readActivePaperDecisions(ctx, tx, sources.declarations.ReceiverID, sources.retained, sources.strategies, sources.feed, eval)
	if err != nil {
		t.Fatal(err)
	}
	verify(sources, func(n int) (ownedAllocationSegment, error) { return readOwnedAllocationSegment(ctx, tx, n) }, p.ownedExecution(sources.feed))
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	for _, table := range []string{"mantik_owned_allocation_checkpoints", "mantik_owned_allocation_batches", "mantik_owned_allocation_members", "mantik_owned_allocation_decision_refs", "mantik_owned_allocation_financial_refs"} {
		if _, err := p.pool.Exec(ctx, "DELETE FROM "+table); err == nil {
			t.Fatal("original index table permits deletion", table)
		}
	}
	if _, err := p.pool.Exec(ctx, `UPDATE mantik_owned_allocations SET payload=convert_to((convert_from(payload,'UTF8')::jsonb-'checkpoint')::text,'UTF8') WHERE singleton`); err == nil {
		t.Fatal("running predecessor discarded an original checkpoint")
	}
	if _, err := p.pool.Exec(ctx, "INSERT INTO mantik_owned_allocation_decision_refs(sequence,decision_id,decision_sha256) VALUES(1,'invented',$1)", workerplan.Hash("invented decision")); err == nil {
		t.Fatal("late reference borrowed an original segment")
	}
	if _, err := p.pool.Exec(ctx, "INSERT INTO mantik_owned_allocation_financial_refs(sequence,decision_id) VALUES(1,'invented')"); err == nil {
		t.Fatal("financial reference borrowed another decision")
	}
	// Only this disposable fixture disables the immutable guard to exercise
	// read-side detection after storage damage; normal deletion above fails.
	if _, err := p.pool.Exec(ctx, `ALTER TABLE mantik_owned_allocation_financial_refs DISABLE TRIGGER mantik_immutable_allocation_financial_refs;
DELETE FROM mantik_owned_allocation_financial_refs WHERE sequence=1;
ALTER TABLE mantik_owned_allocation_financial_refs ENABLE TRIGGER mantik_immutable_allocation_financial_refs`); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if _, err := p.pool.Exec(ctx, `INSERT INTO mantik_owned_allocation_financial_refs(sequence,decision_id)
SELECT a.sequence,d.decision_id FROM mantik_owned_allocation_archive a
CROSS JOIN LATERAL mantik_allocation_financial_decisions(convert_from(a.payload,'UTF8')::jsonb) d WHERE a.sequence=1`); err != nil {
			t.Fatal("restore financial selection fixture", err)
		}
	}()
	if got, err := p.AuditOwnedAllocations(ctx, eval); !errors.Is(err, ErrOwnedAllocationAudit) || !reflect.DeepEqual(got, OwnedAllocationAudit{}) {
		t.Fatal("full audit acknowledged missing financial index rows", err)
	}
}
