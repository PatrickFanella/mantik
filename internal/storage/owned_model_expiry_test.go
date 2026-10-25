package storage

import (
	"context"
	"testing"
)

func executionModelExpiryMigration(t *testing.T, p *Postgres) {
	t.Helper()
	ctx := context.Background()
	var before, after, marker, hash string
	query := "SELECT coalesce(jsonb_agg(payload ORDER BY singleton),'[]'::jsonb)::text FROM mantik_owned_allocations"
	if err := p.pool.QueryRow(ctx, query).Scan(&before); err != nil {
		t.Fatal(err)
	}
	if _, err := p.pool.Exec(ctx, `DELETE FROM mantik_schema WHERE version>=65; COMMENT ON TABLE mantik_owned_allocations IS 'schema64 model expiry fixture'; CREATE FUNCTION reject_model_expiry_migration() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.version=65 THEN RAISE EXCEPTION 'model expiry migration rollback'; END IF; RETURN NEW; END $$; CREATE TRIGGER reject_model_expiry_migration BEFORE INSERT ON mantik_schema FOR EACH ROW EXECUTE FUNCTION reject_model_expiry_migration()`); err != nil {
		t.Fatal(err)
	}
	if err := p.migrate(ctx); err == nil {
		t.Fatal("migration failure ignored")
	}
	if err := p.pool.QueryRow(ctx, "SELECT obj_description('mantik_owned_allocations'::regclass)").Scan(&marker); err != nil || marker != "schema64 model expiry fixture" {
		t.Fatal("migration comment escaped rollback", marker, err)
	}
	if _, err := p.pool.Exec(ctx, "DROP TRIGGER reject_model_expiry_migration ON mantik_schema; DROP FUNCTION reject_model_expiry_migration()"); err != nil {
		t.Fatal(err)
	}
	if err := p.migrate(ctx); err != nil {
		t.Fatal(err)
	}
	if err := p.migrate(ctx); err != nil {
		t.Fatal("migration not idempotent", err)
	}
	if err := p.pool.QueryRow(ctx, query).Scan(&after); err != nil || before != after {
		t.Fatal("migration rewrote allocation payloads", err)
	}
	if err := p.pool.QueryRow(ctx, "SELECT sha256 FROM mantik_schema WHERE version=65").Scan(&hash); err != nil {
		t.Fatal(err)
	}
	if _, err := p.pool.Exec(ctx, "UPDATE mantik_schema SET sha256='damaged' WHERE version=65"); err != nil {
		t.Fatal(err)
	}
	if err := p.migrate(ctx); err == nil {
		t.Fatal("model expiry migration checksum corruption accepted")
	}
	if _, err := p.pool.Exec(ctx, "UPDATE mantik_schema SET sha256=$1 WHERE version=65", hash); err != nil {
		t.Fatal(err)
	}
}
