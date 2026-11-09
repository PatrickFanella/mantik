package storage

import (
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/feedcollector"
	"git.subcult.tv/subculture-collective/mantik/migrations"
)

func TestMemoryPaperRecordScopes(t *testing.T) {
	paperRecordScopeContract(t, NewMemory())
}

func TestPostgresContractsPaperRecordScopes(t *testing.T) {
	paperRecordScopeContract(t, timerPostgres(t, "paperrecordscopes"))
}

func paperRecordScopeContract(t *testing.T, s paperArchiveBackend) {
	t.Helper()
	ctx := t.Context()
	plan, _ := declarationFixture(t, s, "prospective_market_unqualified")
	at := plan.Plan.WindowStart
	now := func() time.Time { return at }
	reg := feedcollector.Registration{ID: "scope-feed", Provider: "alpaca", Feed: "iex", Symbol: "SPY", EvidenceClass: "prospective_market_unqualified", JournalID: "journal-one", AccessRef: "fixture-only", LeaseSeconds: 300}
	for _, c := range []feedcollector.Command{{RequestID: "register", Operation: "register", RegistrationID: reg.ID, Registration: &reg}, {RequestID: "claim", Operation: "claim", RegistrationID: reg.ID, OwnerID: "worker"}} {
		if _, err := s.FeedCollectorCommand(ctx, plan.ReceiverID, c, now); err != nil {
			t.Fatal(err)
		}
	}
	de := func(PaperDecisionCommand, PaperDecisionBasis, time.Time) (PaperDecisionEvaluation, error) {
		return PaperDecisionEvaluation{Action: "abstain", ReasonCode: "scope_fixture_only"}, nil
	}
	oe := func(PaperOutcomeBasis, time.Time) (PaperOutcomeEvaluation, error) {
		return PaperOutcomeEvaluation{Status: "not_applicable", ReasonCode: "scope_fixture_only"}, nil
	}
	at = at.Add(time.Nanosecond)
	d, err := s.FinalizePaperDecision(ctx, PaperDecisionCommand{ID: "scope-decision", ReceiverID: plan.ReceiverID, StrategyID: plan.Plan.Variants[0].ID, StrategySHA256: plan.Plan.Variants[0].SHA256, RegistrationID: reg.ID}, now, de)
	if err != nil {
		t.Fatal(err)
	}
	at = at.Add(time.Nanosecond)
	if _, err := s.AssessPaperOutcome(ctx, PaperOutcomeCommand{ID: "scope-outcome", ReceiverID: plan.ReceiverID, DecisionID: d.Record.Command.ID, DecisionSHA256: d.Record.SHA256}, now, de, oe); err != nil {
		t.Fatal(err)
	}
	at = at.Add(time.Nanosecond)
	if _, err := s.ArchivePaperFeed(ctx, plan.ReceiverID, now, func(FeedEvidenceSnapshot, time.Time) error { return nil }, de, oe); err != nil {
		t.Fatal(err)
	}
	at = at.Add(time.Nanosecond)
	if _, err := s.ArchivePaperRecords(ctx, plan.ReceiverID, now, de, oe); err != nil {
		t.Fatal(err)
	}
	original, err := s.PaperRecordArchive(ctx, 1, de, oe)
	if err != nil {
		t.Fatal(err)
	}
	want := paperScopeForDecision(d.Record)
	checkScopes := func() {
		t.Helper()
		for _, r := range original.Records {
			got, err := paperArchivedScope(r)
			if err != nil || got != want {
				t.Fatal("original native scope", r.Kind, got, want, err)
			}
			if m, ok := s.(*Memory); ok {
				if m.paperRecordScopes[r.Kind+":"+r.ID] != want {
					t.Fatal("memory scope differs from original owner")
				}
			} else {
				var scope paperRecordScope
				var seconds int64
				var nanos int
				err := s.(*Postgres).pool.QueryRow(ctx, "SELECT strategy_id,strategy_sha256,decision_id,decision_sha256,decision_at_seconds,decision_at_nanos FROM mantik_paper_record_epochs WHERE kind=$1 AND record_id=$2", r.Kind, r.ID).Scan(&scope.StrategyID, &scope.StrategySHA256, &scope.DecisionID, &scope.DecisionSHA256, &seconds, &nanos)
				scope.DecisionAt = time.Unix(seconds, int64(nanos)).UTC()
				if err != nil || scope != want {
					t.Fatal("SQL scope differs from original owner", scope, want, err)
				}
			}
		}
	}
	checkScopes()
	if pg, ok := s.(*Postgres); ok {
		// Recreate the actual schema-106 predecessor with original owners and
		// archives still present. Only disposable fixture DDL is removed.
		if _, err := pg.pool.Exec(ctx, "ALTER TABLE mantik_paper_record_epochs DROP CONSTRAINT mantik_paper_scope_shape, DROP COLUMN strategy_id, DROP COLUMN strategy_sha256, DROP COLUMN decision_id, DROP COLUMN decision_sha256, DROP COLUMN decision_at_seconds, DROP COLUMN decision_at_nanos; DELETE FROM mantik_schema WHERE version=107"); err != nil {
			t.Fatal(err)
		}
		start := strings.Index(migrations.PaperRecordArchives, "CREATE OR REPLACE FUNCTION mantik_record_paper_epoch()")
		if start < 0 {
			t.Fatal("predecessor epoch trigger fixture missing")
		}
		end := strings.Index(migrations.PaperRecordArchives[start:], "DROP TRIGGER IF EXISTS mantik_decision_creation_epoch")
		if end < 0 {
			t.Fatal("predecessor epoch trigger fixture end missing")
		}
		if _, err := pg.pool.Exec(ctx, migrations.PaperRecordArchives[start:start+end]); err != nil {
			t.Fatal(err)
		}
		if err := pg.migrate(ctx); err != nil {
			t.Fatal("populated schema-106 scope backfill", err)
		}
		checkScopes()
		if _, err := pg.pool.Exec(ctx, "INSERT INTO mantik_paper_record_epochs(kind,record_id,created_epoch,record_sha256,at_seconds,at_nanos) SELECT kind,record_id,0,record_sha256,at_seconds,at_nanos FROM mantik_paper_record_epochs WHERE kind IN ('decision','outcome') ON CONFLICT(kind,record_id) DO NOTHING"); err != nil {
			t.Fatal("duplicate predecessor backfill lost original scope", err)
		}
		checkScopes()
		for _, sql := range []string{
			"INSERT INTO mantik_paper_record_epochs(kind,record_id,created_epoch,record_sha256,at_seconds,at_nanos) SELECT kind,'missing-scope',created_epoch,record_sha256,at_seconds,at_nanos FROM mantik_paper_record_epochs WHERE kind='decision' ON CONFLICT(kind,record_id) DO NOTHING",
			"INSERT INTO mantik_paper_record_epochs(kind,record_id,created_epoch,record_sha256,at_seconds,at_nanos) SELECT kind,record_id,created_epoch,repeat('0',64),at_seconds,at_nanos FROM mantik_paper_record_epochs WHERE kind='decision' ON CONFLICT(kind,record_id) DO NOTHING",
		} {
			if _, err := pg.pool.Exec(ctx, sql); err == nil || !strings.Contains(err.Error(), "scope missing or changed") {
				t.Fatal("missing or changed reservation borrowed predecessor scope", err)
			}
		}
		if _, err := pg.pool.Exec(ctx, "UPDATE mantik_paper_record_epochs SET strategy_id='changed'"); err == nil {
			t.Fatal("ordinary writer changed immutable scope")
		}
		// A writer that inserts only its original envelope still gets a native
		// scope in the same transaction. Rollback must remove both reservations.
		var later retainedPaperDecision
		if err := json.Unmarshal(original.Records[0].Payload, &later); err != nil {
			t.Fatal(err)
		}
		command := later.Record.Command
		command.ID = "scope-later"
		later.Record, err = paperDecisionForSchema(later.Record.Schema, command, later.Basis, at.Add(time.Nanosecond), de)
		if err != nil {
			t.Fatal(err)
		}
		raw, _ := json.Marshal(later)
		tx, err := pg.pool.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback(ctx)
		if _, err := tx.Exec(ctx, "INSERT INTO mantik_paper_decisions(id,payload) VALUES($1,$2)", command.ID, raw); err != nil {
			t.Fatal("predecessor envelope writer", err)
		}
		var decisionID string
		if err := tx.QueryRow(ctx, "SELECT decision_id FROM mantik_paper_record_epochs WHERE kind='decision' AND record_id=$1", command.ID).Scan(&decisionID); err != nil || decisionID != command.ID {
			t.Fatal("predecessor writer omitted scope", err)
		}
		_ = tx.Rollback(ctx)
		var count int
		if err := pg.pool.QueryRow(ctx, "SELECT count(*) FROM mantik_paper_record_epochs WHERE record_id=$1", command.ID).Scan(&count); err != nil || count != 0 {
			t.Fatal("rollback retained scope reservation", count, err)
		}
		if _, err := pg.pool.Exec(ctx, "DELETE FROM mantik_schema WHERE version=107"); err != nil {
			t.Fatal(err)
		}
		if err := pg.migrate(ctx); err != nil {
			t.Fatal("populated scope DDL replay", err)
		}
		checkScopes()
		tx, err = pg.pool.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback(ctx)
		if _, err := tx.Exec(ctx, "SET LOCAL enable_seqscan=off"); err != nil {
			t.Fatal(err)
		}
		var queryPlan string
		if err := tx.QueryRow(ctx, "EXPLAIN (FORMAT JSON) SELECT record_id FROM mantik_paper_record_epochs WHERE kind IN ('decision','outcome') AND strategy_id=$1 AND strategy_sha256=$2 AND (decision_at_seconds,decision_at_nanos)>=($3,$4) ORDER BY decision_at_seconds,decision_at_nanos,decision_id,kind,at_seconds,at_nanos,record_id LIMIT 17", want.StrategyID, want.StrategySHA256, want.DecisionAt.Unix(), want.DecisionAt.Nanosecond()).Scan(&queryPlan); err != nil || !strings.Contains(queryPlan, "mantik_paper_sample_window") {
			t.Fatal("window selection cannot use owner scope index", queryPlan, err)
		}
		_ = tx.Rollback(ctx)
		connection := pg.pool.Config().ConnString()
		pg.Close()
		reopened, err := OpenPostgres(ctx, connection)
		if err != nil {
			t.Fatal("scope metadata restart", err)
		}
		t.Cleanup(reopened.Close)
		s = reopened
		checkScopes()
	}
	if got, err := s.PaperRecordArchive(ctx, 1, de, oe); err != nil || !reflect.DeepEqual(got, original) {
		t.Fatal("scope metadata changed original archive or hashes", err)
	}
	// Even a syntactically valid projection cannot replace the original owner.
	if m, ok := s.(*Memory); ok {
		v := m.paperRecordScopes["decision:scope-decision"]
		v.StrategyID = "changed"
		m.paperRecordScopes["decision:scope-decision"] = v
	} else {
		pg := s.(*Postgres)
		if _, err := pg.pool.Exec(ctx, "ALTER TABLE mantik_paper_record_epochs DISABLE TRIGGER mantik_immutable_paper_epochs; UPDATE mantik_paper_record_epochs SET strategy_id='changed' WHERE kind='decision'; ALTER TABLE mantik_paper_record_epochs ENABLE TRIGGER mantik_immutable_paper_epochs; DELETE FROM mantik_schema WHERE version=107"); err != nil {
			t.Fatal(err)
		}
		if err := pg.migrate(ctx); err == nil {
			t.Fatal("DDL replay repaired a corrupt original scope")
		}
	}
	if got, err := s.PaperRecordArchive(ctx, 1, de, oe); !errors.Is(err, ErrPaperRecordArchiveConflict) || !reflect.DeepEqual(got, PaperRecordArchive{}) {
		t.Fatal("corrupt scope returned archive success", err)
	}
}
