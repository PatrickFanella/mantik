package storage

import (
	"context"
	"testing"
)

func executionSafetyBindingMigration(t *testing.T, p *Postgres) {
	t.Helper()
	ctx := context.Background()
	var before, after, marker, hash string
	query := "SELECT coalesce(jsonb_agg(payload ORDER BY singleton),'[]'::jsonb)::text FROM mantik_owned_allocations"
	if err := p.pool.QueryRow(ctx, query).Scan(&before); err != nil {
		t.Fatal(err)
	}
	if _, err := p.pool.Exec(ctx, `DELETE FROM mantik_schema WHERE version>=63; COMMENT ON TABLE mantik_owned_allocations IS 'schema62 safety binding fixture'; CREATE FUNCTION reject_safety_binding_migration() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.version=63 THEN RAISE EXCEPTION 'safety binding migration rollback'; END IF; RETURN NEW; END $$; CREATE TRIGGER reject_safety_binding_migration BEFORE INSERT ON mantik_schema FOR EACH ROW EXECUTE FUNCTION reject_safety_binding_migration()`); err != nil {
		t.Fatal(err)
	}
	if err := p.migrate(ctx); err == nil {
		t.Fatal("migration failure ignored")
	}
	if err := p.pool.QueryRow(ctx, "SELECT obj_description('mantik_owned_allocations'::regclass)").Scan(&marker); err != nil || marker != "schema62 safety binding fixture" {
		t.Fatal("migration comment escaped rollback", marker, err)
	}
	if _, err := p.pool.Exec(ctx, "DROP TRIGGER reject_safety_binding_migration ON mantik_schema; DROP FUNCTION reject_safety_binding_migration()"); err != nil {
		t.Fatal(err)
	}
	if err := p.migrate(ctx); err != nil {
		t.Fatal(err)
	}
	if err := p.migrate(ctx); err != nil {
		t.Fatal("migration not idempotent", err)
	}
	if err := p.pool.QueryRow(ctx, query).Scan(&after); err != nil || before != after {
		t.Fatal("migration rewrote assessment payloads", err)
	}
	if err := p.pool.QueryRow(ctx, "SELECT sha256 FROM mantik_schema WHERE version=63").Scan(&hash); err != nil {
		t.Fatal(err)
	}
	if _, err := p.pool.Exec(ctx, "UPDATE mantik_schema SET sha256='damaged' WHERE version=63"); err != nil {
		t.Fatal(err)
	}
	if err := p.migrate(ctx); err == nil {
		t.Fatal("safety binding migration checksum corruption accepted")
	}
	if _, err := p.pool.Exec(ctx, "UPDATE mantik_schema SET sha256=$1 WHERE version=63", hash); err != nil {
		t.Fatal(err)
	}
}
