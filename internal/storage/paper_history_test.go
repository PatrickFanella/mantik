package storage

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/feedcollector"
)

type paperHistoryBackend interface {
	paperTargetBackend
	PaperHistoryStore
}

func TestMemoryPaperHistory(t *testing.T) {
	paperHistoryContract(t, NewMemory())
}

func TestPostgresContractsPaperHistory(t *testing.T) {
	paperHistoryContract(t, timerPostgres(t, "paperhistory"))
}

func TestPostgresContractsPaperHistoryUpgrade(t *testing.T) {
	p := timerPostgres(t, "paperhistoryupgrade")
	ctx := t.Context()
	if _, err := p.pool.Exec(ctx, `DROP INDEX mantik_paper_history_page;
DROP TABLE mantik_owned_allocation_financial_refs;
DELETE FROM mantik_schema WHERE version>=110;
CREATE FUNCTION reject_paper_history_migration() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.version=110 THEN RAISE EXCEPTION 'history index migration rollback fixture'; END IF; RETURN NEW; END $$;
CREATE TRIGGER reject_paper_history_migration BEFORE INSERT ON mantik_schema FOR EACH ROW EXECUTE FUNCTION reject_paper_history_migration()`); err != nil {
		t.Fatal(err)
	}
	if err := p.migrate(ctx); err == nil {
		t.Fatal("failed history index migration committed")
	}
	var exists bool
	if err := p.pool.QueryRow(ctx, "SELECT to_regclass('mantik_paper_history_page') IS NOT NULL").Scan(&exists); err != nil || exists {
		t.Fatal("failed migration left the history index", err)
	}
	if _, err := p.pool.Exec(ctx, "DROP TRIGGER reject_paper_history_migration ON mantik_schema; DROP FUNCTION reject_paper_history_migration()"); err != nil {
		t.Fatal(err)
	}
	if err := p.migrate(ctx); err != nil {
		t.Fatal(err)
	}
	var sha string
	if err := p.pool.QueryRow(ctx, "SELECT sha256 FROM mantik_schema WHERE version=110").Scan(&sha); err != nil {
		t.Fatal(err)
	}
	if _, err := p.pool.Exec(ctx, "UPDATE mantik_schema SET sha256='damaged' WHERE version=110"); err != nil {
		t.Fatal(err)
	}
	if err := p.migrate(ctx); err == nil {
		t.Fatal("changed history migration checksum accepted")
	}
	if _, err := p.pool.Exec(ctx, "UPDATE mantik_schema SET sha256=$1 WHERE version=110", sha); err != nil {
		t.Fatal(err)
	}
	if err := p.migrate(ctx); err != nil {
		t.Fatal(err)
	}
}

func paperHistoryContract(t *testing.T, s paperHistoryBackend) {
	t.Helper()
	ctx := t.Context()
	plan, _ := declarationFixture(t, s, "prospective_market_unqualified")
	at := plan.Plan.WindowStart
	now := func() time.Time { return at }
	reg := feedcollector.Registration{ID: "history-feed", Provider: "alpaca", Feed: "iex", Symbol: "SPY", EvidenceClass: "prospective_market_unqualified", JournalID: "journal-one", AccessRef: "fixture-only", LeaseSeconds: 300}
	for _, c := range []feedcollector.Command{{RequestID: "register", Operation: "register", RegistrationID: reg.ID, Registration: &reg}, {RequestID: "claim", Operation: "claim", RegistrationID: reg.ID, OwnerID: "worker"}} {
		if _, err := s.FeedCollectorCommand(ctx, plan.ReceiverID, c, now); err != nil {
			t.Fatal(err)
		}
	}
	de := func(PaperDecisionCommand, PaperDecisionBasis, time.Time) (PaperDecisionEvaluation, error) {
		return PaperDecisionEvaluation{Action: "abstain", ReasonCode: "history_fixture_only"}, nil
	}
	oe := func(PaperOutcomeBasis, time.Time) (PaperOutcomeEvaluation, error) {
		return PaperOutcomeEvaluation{Status: "not_applicable", ReasonCode: "history_fixture_only"}, nil
	}
	q := PaperHistoryQuery{ReceiverID: plan.ReceiverID, Kind: "decision", Limit: 1}
	if page, err := s.PaperHistory(ctx, q, de, oe); err != nil || page.Validate(q) != nil || page.Through != nil || len(page.Records) != 0 || page.NextAfter != "" {
		t.Fatal("empty sealed history", page, err)
	}
	decisions, outcomes := map[string]PaperDecisionRecord{}, map[string]PaperOutcomeRecord{}
	add := func(id string, outcome bool) {
		t.Helper()
		at = at.Add(time.Nanosecond)
		d, err := s.FinalizePaperDecision(ctx, PaperDecisionCommand{ID: id, ReceiverID: plan.ReceiverID, StrategyID: plan.Plan.Variants[0].ID, StrategySHA256: plan.Plan.Variants[0].SHA256, RegistrationID: reg.ID}, now, de)
		if err != nil {
			t.Fatal("history decision fixture", id, err)
		}
		decisions[id] = d.Record
		if outcome {
			at = at.Add(time.Nanosecond)
			o, err := s.AssessPaperOutcome(ctx, PaperOutcomeCommand{ID: id + "-outcome", ReceiverID: plan.ReceiverID, DecisionID: id, DecisionSHA256: d.Record.SHA256}, now, de, oe)
			if err != nil {
				t.Fatal("history outcome fixture", id, err)
			}
			outcomes[o.Record.Command.ID] = o.Record
		}
	}
	seal := func() {
		t.Helper()
		at = at.Add(time.Nanosecond)
		if _, err := s.RotateFeed(ctx, plan.ReceiverID, now, func(FeedEvidenceSnapshot, time.Time) error { return nil }, de, oe); err != nil {
			t.Fatal("history feed seal", err)
		}
		at = at.Add(time.Nanosecond)
		if _, err := s.ArchivePaperRecords(ctx, plan.ReceiverID, now, de, oe); err != nil {
			t.Fatal("history paper seal", err)
		}
	}
	for _, id := range []string{"tie_a", "tie-a", "tie-A"} {
		add(id, true)
	}
	seal()
	first, err := s.PaperHistory(ctx, q, de, oe)
	if err != nil || first.Validate(q) != nil || len(first.Records) != 1 || first.Records[0].ID != "tie-A" || first.Through.Sequence != 1 || first.NextAfter == "" {
		t.Fatal("first original page", first, err)
	}
	add("later", false)
	seal()
	add("unsealed", false)
	// Advancing the actual archive must not expand a cursor's earlier closed
	// creation interval, even when later IDs sort before the original ones.
	seen := []string{"tie-A"}
	for cursor := first.NextAfter; cursor != ""; {
		query := q
		query.After = cursor
		page, err := s.PaperHistory(ctx, query, de, oe)
		if err != nil || page.Validate(query) != nil || page.Through.SHA256 != first.Through.SHA256 || len(page.Records) != 1 {
			t.Fatal("stable original boundary", page, err)
		}
		item := page.Records[0]
		if !reflect.DeepEqual(*item.Decision, decisions[item.ID]) || item.CreatedEpoch != 0 {
			t.Fatal("original decision changed", item)
		}
		seen = append(seen, item.ID)
		cursor = page.NextAfter
	}
	if !reflect.DeepEqual(seen, []string{"tie-A", "tie-a", "tie_a"}) {
		t.Fatal("native byte order or page completeness", seen)
	}
	allQuery := q
	allQuery.Limit = 64
	all, err := s.PaperHistory(ctx, allQuery, de, oe)
	if err != nil || all.Validate(allQuery) != nil || all.Through.Sequence != 2 || len(all.Records) != 4 || all.Records[3].ID != "later" || all.NextAfter != "" {
		t.Fatal("new boundary or open interval exclusion", all, err)
	}
	if p, ok := s.(*Postgres); ok {
		tx, err := p.pool.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback(ctx)
		if _, err := tx.Exec(ctx, "SET LOCAL enable_seqscan=off; SET LOCAL enable_bitmapscan=off"); err != nil {
			t.Fatal(err)
		}
		rows, err := tx.Query(ctx, "EXPLAIN (COSTS OFF) "+paperHistoryPageQuery, "decision", all.Through.Sequence, int64(0), "", 3)
		if err != nil {
			t.Fatal(err)
		}
		var plan []string
		for rows.Next() {
			var line string
			if err := rows.Scan(&line); err != nil {
				t.Fatal(err)
			}
			plan = append(plan, line)
		}
		err = rows.Err()
		rows.Close()
		if err != nil || !strings.Contains(strings.Join(plan, "\n"), "mantik_paper_history_page") || strings.Contains(strings.Join(plan, "\n"), "Sort") {
			t.Fatal("history cursor cannot seek its ordered bounded index", plan, err)
		}
		if err := tx.Rollback(ctx); err != nil {
			t.Fatal(err)
		}
	}
	outcomeQuery := allQuery
	outcomeQuery.Kind = "outcome"
	gotOutcomes, err := s.PaperHistory(ctx, outcomeQuery, de, oe)
	if err != nil || gotOutcomes.Validate(outcomeQuery) != nil || len(gotOutcomes.Records) != 3 {
		t.Fatal("outcome history", gotOutcomes, err)
	}
	for _, r := range gotOutcomes.Records {
		if !reflect.DeepEqual(*r.Outcome, outcomes[r.ID]) {
			t.Fatal("original outcome changed", r.ID)
		}
	}
	for _, mutate := range []func(*PaperHistoryQuery){
		func(q *PaperHistoryQuery) { q.Kind = "review" },
		func(q *PaperHistoryQuery) { q.Limit = 0 },
		func(q *PaperHistoryQuery) { q.Limit = 65 },
		func(q *PaperHistoryQuery) { q.After = "invalid" },
		func(q *PaperHistoryQuery) { q.After = strings.Repeat("x", 4097) },
		func(q *PaperHistoryQuery) { q.After = first.NextAfter; q.Kind = "outcome" },
		func(q *PaperHistoryQuery) { q.After = first.NextAfter; q.ReceiverID = "receiver-other" },
	} {
		bad := q
		mutate(&bad)
		if got, err := s.PaperHistory(ctx, bad, de, oe); err == nil || !reflect.DeepEqual(got, PaperHistoryPage{}) {
			t.Fatal("invalid query returned evidence", err)
		}
	}
	changed := q
	changed.After = first.NextAfter
	cursor, err := changed.cursor()
	if err != nil {
		t.Fatal(err)
	}
	cursor.ThroughSHA256 = strings.Repeat("0", 64)
	rawCursor, _ := json.Marshal(cursor)
	changed.After = base64.RawURLEncoding.EncodeToString(rawCursor)
	if got, err := s.PaperHistory(ctx, changed, de, oe); err == nil || !reflect.DeepEqual(got, PaperHistoryPage{}) {
		t.Fatal("cursor changed the actual original boundary", err)
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if got, err := s.PaperHistory(canceled, q, de, oe); !errors.Is(err, context.Canceled) || !reflect.DeepEqual(got, PaperHistoryPage{}) {
		t.Fatal("cancellation returned partial history", err)
	}
	wrongEval := func(PaperDecisionCommand, PaperDecisionBasis, time.Time) (PaperDecisionEvaluation, error) {
		return PaperDecisionEvaluation{Action: "abstain", ReasonCode: "counterfeit_replay"}, nil
	}
	if got, err := s.PaperHistory(ctx, allQuery, wrongEval, oe); err == nil || !reflect.DeepEqual(got, PaperHistoryPage{}) {
		t.Fatal("presentation hash replaced native evaluation", err)
	}
	// A missing active row cannot silently fall back to its archived duplicate.
	if m, ok := s.(*Memory); ok {
		m.mu.Lock()
		old := m.paperDecisions["tie-A"]
		delete(m.paperDecisions, "tie-A")
		m.mu.Unlock()
		got, err := s.PaperHistory(ctx, allQuery, de, oe)
		m.mu.Lock()
		m.paperDecisions["tie-A"] = old
		m.mu.Unlock()
		if err == nil || !reflect.DeepEqual(got, PaperHistoryPage{}) {
			t.Fatal("missing active owner was hidden", err)
		}
	} else {
		p := s.(*Postgres)
		var raw []byte
		if err := p.pool.QueryRow(ctx, "DELETE FROM mantik_paper_decisions WHERE id='tie-A' RETURNING payload").Scan(&raw); err != nil {
			t.Fatal(err)
		}
		got, readErr := s.PaperHistory(ctx, allQuery, de, oe)
		tx, err := p.pool.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback(ctx)
		if _, err := tx.Exec(ctx, "ALTER TABLE mantik_paper_decisions DISABLE TRIGGER USER"); err != nil {
			t.Fatal(err)
		}
		if _, err := tx.Exec(ctx, "INSERT INTO mantik_paper_decisions(id,payload) VALUES('tie-A',$1)", raw); err != nil {
			t.Fatal(err)
		}
		if _, err := tx.Exec(ctx, "ALTER TABLE mantik_paper_decisions ENABLE TRIGGER USER"); err != nil {
			t.Fatal(err)
		}
		if err := tx.Commit(ctx); err != nil {
			t.Fatal(err)
		}
		if readErr == nil || !reflect.DeepEqual(got, PaperHistoryPage{}) {
			t.Fatal("missing active owner was hidden", readErr)
		}
	}
	// Only a disposable fixture installs a future approved retirement. The
	// production immutable guard remains enabled and no retirement API exists.
	if m, ok := s.(*Memory); ok {
		m.mu.Lock()
		m.paperRecordRetiredEpochs = map[string]int64{}
		for _, kind := range []string{"decision", "outcome"} {
			for _, id := range []string{"tie-A", "tie-a", "tie_a"} {
				if kind == "outcome" {
					id += "-outcome"
					delete(m.paperOutcomes, id)
				} else {
					delete(m.paperDecisions, id)
				}
				m.paperRecordRetiredEpochs[kind+":"+id] = 2
			}
		}
		m.mu.Unlock()
	} else if _, err := s.(*Postgres).pool.Exec(ctx, `ALTER TABLE mantik_paper_record_epochs DISABLE TRIGGER mantik_immutable_paper_epochs;
UPDATE mantik_paper_record_epochs SET retired_epoch=2 WHERE created_epoch=0 AND kind IN ('decision','outcome');
ALTER TABLE mantik_paper_record_epochs ENABLE TRIGGER mantik_immutable_paper_epochs;
DELETE FROM mantik_paper_outcomes; DELETE FROM mantik_paper_decisions WHERE id IN ('tie-A','tie-a','tie_a')`); err != nil {
		t.Fatal(err)
	}
	if got, err := s.PaperHistory(ctx, allQuery, de, oe); err != nil || !reflect.DeepEqual(got, all) {
		t.Fatal("retirement changed fixed original history", err)
	}
	if got, err := s.PaperHistory(ctx, outcomeQuery, de, oe); err != nil || !reflect.DeepEqual(got, gotOutcomes) {
		t.Fatal("retired outcome decision dependency was lost", err)
	}
	all.Records[0].Decision.Evaluation.ReasonCode = "caller_mutation"
	if got, err := s.PaperHistory(ctx, allQuery, de, oe); err != nil || got.Records[0].Decision.Evaluation.ReasonCode != "history_fixture_only" {
		t.Fatal("returned history aliases owner records", err)
	}
}
