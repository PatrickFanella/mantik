package storage

import (
	"reflect"
	"testing"
)

// Runs on a populated disposable owner, before deliberate source corruption in
// the surrounding checkpoint contract. The predecessor retains all originals.
func verifyAllocationFinancialUpgrade(t *testing.T, p *Postgres, de PaperDecisionEvaluator, all OwnedAllocationCatalog) {
	t.Helper()
	ctx := t.Context()
	fingerprint := func() []string {
		t.Helper()
		rows, err := p.pool.Query(ctx, `SELECT 'archive:'||sequence::text||':'||encode(sha256(payload),'hex')
FROM mantik_owned_allocation_archive
UNION ALL SELECT 'checkpoint:'||sequence::text||':'||encode(sha256(payload),'hex') FROM mantik_owned_allocation_checkpoints
UNION ALL SELECT 'journal:'||encode(sha256(payload),'hex') FROM mantik_owned_allocations ORDER BY 1`)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		result := []string{}
		for rows.Next() {
			var hash string
			if err := rows.Scan(&hash); err != nil {
				t.Fatal(err)
			}
			result = append(result, hash)
		}
		if err := rows.Err(); err != nil {
			t.Fatal(err)
		}
		return result
	}
	before := fingerprint()
	predecessor := func() {
		t.Helper()
		if _, err := p.pool.Exec(ctx, `DROP TRIGGER mantik_allocation_financial_refs_complete ON mantik_owned_allocation_members;
DROP TABLE mantik_owned_allocation_financial_refs;
DELETE FROM mantik_schema WHERE version=111;`); err != nil {
			t.Fatal(err)
		}
	}
	requireRolledBack := func() {
		t.Helper()
		var exists bool
		if err := p.pool.QueryRow(ctx, "SELECT to_regclass('mantik_owned_allocation_financial_refs') IS NOT NULL").Scan(&exists); err != nil || exists {
			t.Fatal("failed financial migration retained a partial index", err)
		}
		var maximum int
		if err := p.pool.QueryRow(ctx, "SELECT max(version) FROM mantik_schema").Scan(&maximum); err != nil || maximum != 110 {
			t.Fatal("failed financial migration advanced schema", maximum, err)
		}
	}
	predecessor()
	if _, err := p.pool.Exec(ctx, `CREATE FUNCTION reject_financial_migration() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.version=111 THEN RAISE EXCEPTION 'financial migration rollback fixture'; END IF; RETURN NEW; END $$;
CREATE TRIGGER reject_financial_migration BEFORE INSERT ON mantik_schema FOR EACH ROW EXECUTE FUNCTION reject_financial_migration()`); err != nil {
		t.Fatal(err)
	}
	if err := p.migrate(ctx); err == nil {
		t.Fatal("failed financial migration committed its backfill")
	}
	requireRolledBack()
	if !reflect.DeepEqual(before, fingerprint()) {
		t.Fatal("failed financial migration changed original owner bytes")
	}
	if _, err := p.pool.Exec(ctx, "DROP TRIGGER reject_financial_migration ON mantik_schema; DROP FUNCTION reject_financial_migration()"); err != nil {
		t.Fatal(err)
	}
	if err := p.migrate(ctx); err != nil {
		t.Fatal("populated financial-reference backfill", err)
	}
	var count int
	if err := p.pool.QueryRow(ctx, "SELECT count(*) FROM mantik_owned_allocation_financial_refs").Scan(&count); err != nil || count != 6 {
		t.Fatal("financial backfill omitted original indexed comparisons", count, err)
	}
	if got, err := p.OwnedAllocations(ctx, de); err != nil || !reflect.DeepEqual(got, all) {
		t.Fatal("financial upgrade changed original native readback", err)
	}
	if got, err := p.AuditOwnedAllocations(ctx, de); err != nil || got.Status != "verified" {
		t.Fatal("financial backfill replaced native original proof", err)
	}
	var original []byte
	if err := p.pool.QueryRow(ctx, "SELECT payload FROM mantik_owned_allocation_archive WHERE sequence=1").Scan(&original); err != nil {
		t.Fatal(err)
	}
	predecessor()
	if _, err := p.pool.Exec(ctx, `UPDATE mantik_owned_allocation_archive SET payload=convert_to(jsonb_set(convert_from(payload,'UTF8')::jsonb,'{record,at}','"2001-01-01T00:00:00Z"'::jsonb)::text,'UTF8') WHERE sequence=1`); err != nil {
		t.Fatal(err)
	}
	if err := p.migrate(ctx); err == nil {
		t.Fatal("damaged original became financial selection metadata")
	}
	requireRolledBack()
	if _, err := p.pool.Exec(ctx, "UPDATE mantik_owned_allocation_archive SET payload=$1 WHERE sequence=1", original); err != nil {
		t.Fatal(err)
	}
	if err := p.migrate(ctx); err != nil {
		t.Fatal(err)
	}
	var sha string
	if err := p.pool.QueryRow(ctx, "SELECT sha256 FROM mantik_schema WHERE version=111").Scan(&sha); err != nil {
		t.Fatal(err)
	}
	if _, err := p.pool.Exec(ctx, "UPDATE mantik_schema SET sha256='damaged' WHERE version=111"); err != nil {
		t.Fatal(err)
	}
	if err := p.migrate(ctx); err == nil {
		t.Fatal("changed financial migration checksum accepted")
	}
	if _, err := p.pool.Exec(ctx, "UPDATE mantik_schema SET sha256=$1 WHERE version=111", sha); err != nil {
		t.Fatal(err)
	}
	if err := p.migrate(ctx); err != nil || !reflect.DeepEqual(before, fingerprint()) {
		t.Fatal("financial upgrade changed original journal/archive/checkpoint bytes", err)
	}
}
