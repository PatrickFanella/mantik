package storage

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/workerplan"
)

func TestMemoryDistinctEvaluationVariants(t *testing.T) {
	for _, aliases := range []bool{false, true} {
		t.Run(map[bool]string{false: "distinct", true: "aliases"}[aliases], func(t *testing.T) {
			distinctEvaluationVariants(t, NewMemory(), aliases)
		})
	}
}
func TestPostgresContractsDistinctEvaluationVariants(t *testing.T) {
	t.Run("distinct", func(t *testing.T) { distinctEvaluationVariants(t, timerPostgres(t, "evaldistinctvariants"), false) })
	t.Run("aliases", func(t *testing.T) { distinctEvaluationVariants(t, timerPostgres(t, "evalaliasvariants"), true) })
}

func distinctEvaluationVariants(t *testing.T, s evaluationDeclarationBackend, aliases bool) {
	t.Helper()
	ctx := context.Background()
	c, at := declarationVariantsFixture(t, s, !aliases)
	before, err := s.Snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	accounts, err := s.PaperAccounts(ctx)
	if err != nil {
		t.Fatal(err)
	}
	strategies, err := s.PaperStrategies(ctx)
	if err != nil {
		t.Fatal(err)
	}
	policies, err := s.AllocationPolicies(ctx)
	if err != nil {
		t.Fatal(err)
	}
	clock := func() time.Time { return at.Add(time.Second) }
	noClock := func() time.Time { panic("retained variant declaration retry read clock") }
	if aliases {
		got, e := s.DeclarePaperEvaluation(ctx, c, clock)
		if !errors.Is(e, ErrPaperEvaluationDeclarationConflict) || !reflect.DeepEqual(got, PaperEvaluationDeclarationReceipt{}) {
			t.Fatal("different IDs manufactured distinct native configurations", got, e)
		}
		empty, e := s.PaperEvaluationDeclarations(ctx)
		if e != nil || len(empty.Records) != 0 {
			t.Fatal("rejected aliases retained declaration", empty, e)
		}
	} else {
		var clocks atomic.Int32
		var wg sync.WaitGroup
		for range 8 {
			wg.Go(func() {
				if _, e := s.DeclarePaperEvaluation(ctx, c, func() time.Time { clocks.Add(1); return clock() }); e != nil {
					t.Error(e)
				}
			})
		}
		wg.Wait()
		got, e := s.DeclarePaperEvaluation(ctx, c, noClock)
		if e != nil || !got.Duplicate || clocks.Load() != 1 || got.Record.Schema != "mantik-owned-paper-evaluation-declaration-v2" || len(got.Record.VariantConfigurations) != 2 {
			t.Fatal(got, e, clocks.Load())
		}
		if got.Record.VariantConfigurations[0].ConfigurationSHA256 == got.Record.VariantConfigurations[1].ConfigurationSHA256 || got.Record.AdmissionAvailable || got.Record.EvaluationAvailable || got.Record.ExecutionAvailable || got.Record.QualificationCredit {
			t.Fatal("configuration inventory became sensitivity or authority", got)
		}
		for i, binding := range got.Record.VariantConfigurations {
			pin := c.Plan.Variants[i]
			if binding.StrategyID != pin.ID || binding.StrategySHA256 != pin.SHA256 || binding.Method != "native-definition-without-id-v1" {
				t.Fatal("configuration lost owner pin", binding)
			}
		}
		original := clone(got.Record)
		got.Record.VariantConfigurations[0].ConfigurationSHA256 = strings.Repeat("f", 64)
		got.Record.SHA256 = ""
		got.Record.SHA256 = workerplan.Hash(got.Record)
		storeVariantDeclarationFixture(t, s, got.Record)
		if _, e := s.PaperEvaluationDeclarations(ctx); e == nil {
			t.Fatal("self-rehashed configuration substituted actual owner")
		}
		storeVariantDeclarationFixture(t, s, original)
		c.ID = "legacy-declaration"
		c.Plan.FamilyID = "legacy-family"
	}
	// Explicit old-writer fixture: duplicate configurations were legal in v1.
	// Preserve their original count/bytes; do not promote them to v2 validation.
	legacy, err := paperEvaluationDeclarationVersion(c, strategies, policies, clock(), "mantik-owned-paper-evaluation-declaration-v1")
	if err != nil {
		t.Fatal(err)
	}
	legacyBytes, _ := json.Marshal(legacy)
	if bytes.Contains(legacyBytes, []byte("variant_configurations")) || legacy.PlannedVariants != 2 {
		t.Fatal("legacy wire/count changed")
	}
	storeVariantDeclarationFixture(t, s, legacy)
	retry, err := s.DeclarePaperEvaluation(ctx, c, noClock)
	actualBytes, _ := json.Marshal(retry.Record)
	if err != nil || !retry.Duplicate || !bytes.Equal(legacyBytes, actualBytes) {
		t.Fatal("legacy exact retry changed", retry, err)
	}
	all, err := s.PaperEvaluationDeclarations(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if pg, ok := s.(*Postgres); ok {
		reopened, e := OpenPostgres(ctx, pg.pool.Config().ConnString())
		if e != nil {
			t.Fatal(e)
		}
		actual, e := reopened.PaperEvaluationDeclarations(ctx)
		reopened.Close()
		if e != nil || !reflect.DeepEqual(actual, all) {
			t.Fatal("reopen changed versioned declarations", e)
		}
		if !aliases {
			distinctVariantMigration(t, pg)
		}
	}
	after, err := s.Snapshot(ctx)
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatal("variant planning changed operations", err)
	}
	actualAccounts, err := s.PaperAccounts(ctx)
	if err != nil || !reflect.DeepEqual(accounts, actualAccounts) {
		t.Fatal("variant planning changed capital", err)
	}
}

func storeVariantDeclarationFixture(t *testing.T, s evaluationDeclarationBackend, r PaperEvaluationDeclaration) {
	t.Helper()
	if m, ok := s.(*Memory); ok {
		m.paperEvaluationPlans[r.Command.ID] = clone(r)
		return
	}
	pg := s.(*Postgres)
	raw, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = pg.pool.Exec(context.Background(), "INSERT INTO mantik_paper_evaluation_declarations(id,payload) VALUES($1,$2) ON CONFLICT(id) DO UPDATE SET payload=excluded.payload", r.Command.ID, raw); err != nil {
		t.Fatal(err)
	}
}

func distinctVariantMigration(t *testing.T, p *Postgres) {
	t.Helper()
	ctx := context.Background()
	var before, after, marker, hash string
	query := "SELECT coalesce(jsonb_agg(payload ORDER BY id),'[]'::jsonb)::text FROM mantik_paper_evaluation_declarations"
	if err := p.pool.QueryRow(ctx, query).Scan(&before); err != nil {
		t.Fatal(err)
	}
	if _, err := p.pool.Exec(ctx, `DELETE FROM mantik_schema WHERE version>=60; COMMENT ON TABLE mantik_paper_evaluation_declarations IS 'schema59 variant fixture'; CREATE FUNCTION reject_variant_migration() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.version=60 THEN RAISE EXCEPTION 'variant migration rollback'; END IF; RETURN NEW; END $$; CREATE TRIGGER reject_variant_migration BEFORE INSERT ON mantik_schema FOR EACH ROW EXECUTE FUNCTION reject_variant_migration()`); err != nil {
		t.Fatal(err)
	}
	if err := p.migrate(ctx); err == nil {
		t.Fatal("migration failure ignored")
	}
	if err := p.pool.QueryRow(ctx, "SELECT obj_description('mantik_paper_evaluation_declarations'::regclass)").Scan(&marker); err != nil || marker != "schema59 variant fixture" {
		t.Fatal("migration comment escaped rollback", marker, err)
	}
	if _, err := p.pool.Exec(ctx, "DROP TRIGGER reject_variant_migration ON mantik_schema; DROP FUNCTION reject_variant_migration()"); err != nil {
		t.Fatal(err)
	}
	if err := p.migrate(ctx); err != nil {
		t.Fatal(err)
	}
	if err := p.migrate(ctx); err != nil {
		t.Fatal("migration not idempotent", err)
	}
	if err := p.pool.QueryRow(ctx, query).Scan(&after); err != nil || before != after {
		t.Fatal("migration rewrote declaration payloads", err)
	}
	if err := p.pool.QueryRow(ctx, "SELECT sha256 FROM mantik_schema WHERE version=60").Scan(&hash); err != nil {
		t.Fatal(err)
	}
	if _, err := p.pool.Exec(ctx, "UPDATE mantik_schema SET sha256='damaged' WHERE version=60"); err != nil {
		t.Fatal(err)
	}
	if err := p.migrate(ctx); err == nil {
		t.Fatal("variant migration checksum corruption accepted")
	}
	if _, err := p.pool.Exec(ctx, "UPDATE mantik_schema SET sha256=$1 WHERE version=60", hash); err != nil {
		t.Fatal(err)
	}
}
