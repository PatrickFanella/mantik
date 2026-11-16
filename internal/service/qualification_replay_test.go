package service

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"reflect"
	"strings"
	"testing"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/account"
	"git.subcult.tv/subculture-collective/mantik/internal/forward"
	"git.subcult.tv/subculture-collective/mantik/internal/storage"
	"git.subcult.tv/subculture-collective/mantik/internal/strategytrial"
	"github.com/jackc/pgx/v5"
)

type qualificationReplayFailureStore struct {
	qualificationTestStore
	before *account.CappedState
}

func (s *qualificationReplayFailureStore) CollectQualification(ctx context.Context, id string, version int64, c account.BookCommand) (account.CappedState, error) {
	before, e := s.CappedBook(ctx, id)
	if e != nil {
		return account.CappedState{}, e
	}
	s.before = &before
	return s.qualificationTestStore.CollectQualification(ctx, id, version, c)
}

// These are compiled lifecycle fixtures with a local fake venue. The SQL
// failure is injected in the enclosing transaction after custody insertion;
// no model, provider or real broker is contacted.
func TestPostgresContractsQualificationReplayEnclosingFailure(t *testing.T) {
	for _, deferred := range []bool{false, true} {
		name := "statement_rollback"
		if deferred {
			name = "commit_failure"
		}
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			r, req, _, broker, d, plan := qualificationRunnerFixture(t)
			pg := r.Store.(*storage.Postgres)
			sqlURL, e := url.Parse(qualificationPostgresURL(t))
			if e != nil {
				t.Fatal(e)
			}
			query := sqlURL.Query()
			query.Del("pool_max_conns")
			sqlURL.RawQuery = query.Encode()
			sql, e := pgx.Connect(ctx, sqlURL.String())
			if e != nil {
				t.Fatal(e)
			}
			t.Cleanup(func() {
				if e := sql.Close(ctx); e != nil {
					t.Error(e)
				}
			})

			inject := func(kind string) {
				t.Helper()
				timing := "IMMEDIATE"
				if deferred {
					timing = "DEFERRED"
				}
				statement := fmt.Sprintf(`CREATE FUNCTION reject_joined_custody() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN IF NEW.payload->>'Kind'='%s' THEN RAISE EXCEPTION 'joined custody rollback'; END IF; RETURN NEW; END $$;
CREATE CONSTRAINT TRIGGER reject_joined_custody AFTER INSERT ON mantik_capped_book_history DEFERRABLE INITIALLY %s FOR EACH ROW EXECUTE FUNCTION reject_joined_custody()`, kind, timing)
				if _, e := sql.Exec(ctx, statement); e != nil {
					t.Fatal(e)
				}
			}
			removeFailure := func() {
				t.Helper()
				if _, e := sql.Exec(ctx, `DROP TRIGGER reject_joined_custody ON mantik_capped_book_history; DROP FUNCTION reject_joined_custody()`); e != nil {
					t.Fatal(e)
				}
			}
			assertState := func(want account.CappedState) {
				t.Helper()
				// The original store has a warm cache; a new instance must derive the same
				// committed bytes from originals. Neither can see the aborted transition.
				cold, e := storage.OpenPostgresWithQualificationEvaluator(ctx, qualificationPostgresURL(t), EvaluateQualificationCollection)
				if e != nil {
					t.Fatal(e)
				}
				defer cold.Close()
				for _, reader := range []*storage.Postgres{pg, cold} {
					got, e := reader.CappedBook(ctx, r.BookID)
					if e != nil || !reflect.DeepEqual(got, want) {
						t.Fatal("aborted custody escaped through warm/cold replay", e)
					}
				}
				rebuilt, e := storage.ReplayCappedBookHistory(ctx, pg, want.Config, want.Version)
				if e != nil || !reflect.DeepEqual(rebuilt, want) {
					t.Fatal("immutable recovery differs", e)
				}
				var n int64
				if e := sql.QueryRow(ctx, `SELECT coalesce(max(sequence),0) FROM mantik_capped_book_history WHERE book_id=$1`, r.BookID).Scan(&n); e != nil || n != want.Version {
					t.Fatal("failed transaction retained a sequence", n, want.Version, e)
				}
			}
			reuseSequence := func(before account.CappedState, id string, at time.Time) account.CappedState {
				t.Helper()
				// Use different bytes at the aborted sequence, so stale unpublished state
				// cannot be mistaken for a successful exact-command retry.
				next, e := pg.ApplyCappedBook(ctx, r.BookID, before.Version, account.BookCommand{ID: id, Kind: "risk_observe", At: at, Marks: map[string]string{"SPY": "100"}})
				if e != nil || next.Version != before.Version+1 {
					t.Fatal("aborted sequence was not reusable", e)
				}
				page, e := pg.CappedBookHistory(ctx, r.BookID, before.Version, 1)
				if e != nil || len(page) != 1 || page[0].Sequence != next.Version || page[0].Command.ID != id {
					t.Fatal("replacement custody", e)
				}
				assertState(next)
				return next
			}

			inject("qualification_plan")
			wrapped := &qualificationReplayFailureStore{qualificationTestStore: pg}
			r.Store = wrapped
			first := nextForwardCycle(req, "failed-first-collection", plan.HoldoutStart)
			first.Qualification = &forward.Collection{PlanID: plan.ID, Benchmark: qualificationBenchmark(t, d, first.ExecuteAt)}
			first = compiledQualificationRequest(t, first, true)
			r.now = func() time.Time { return first.ExecuteAt }
			healthyIncubationWorkers(t, r, first.ExecuteAt)
			if _, e := r.Run(ctx, first); e == nil || !strings.Contains(e.Error(), "joined custody rollback") {
				t.Fatal("collection did not reach injected failure", e)
			}
			if wrapped.before == nil || broker.posts != 0 {
				t.Fatal("failure missed collection boundary or reached venue", broker.posts)
			}
			assertState(*wrapped.before)
			removeFailure()
			reuseSequence(*wrapped.before, "reuse-after-collection", first.ExecuteAt)
			r.Store = pg
			// Continue with a later decision clock: the independent safety cycle
			// must never be rewound behind the committed replacement observation.
			collectQualificationRunnerEvidence(t, r, req, broker, d, plan, first.ExecuteAt.Add(time.Second))
			raw, e := json.Marshal(d.Spec)
			if e != nil {
				t.Fatal(e)
			}
			if _, e := pg.StartStrategyTrial(ctx, strategytrial.Start{ID: "actual-search", Family: d.Family, StrategyID: d.StrategyID, Spec: raw, Config: json.RawMessage(`{"contract_fixture":true}`), DataSHA256: incubationDigest("trial-data"), EvidenceClass: "synthetic_fixture", StartedAt: d.WindowStart.Add(-time.Second / 2)}); e != nil {
				t.Fatal(e)
			}
			before, e := pg.CappedBook(ctx, r.BookID)
			if e != nil {
				t.Fatal(e)
			}
			assertState(before)

			inject("qualification")
			if _, e := pg.AssessForward(ctx, plan.ID, func() time.Time { return d.OutcomeDeadline }); e == nil || !strings.Contains(e.Error(), "joined custody rollback") {
				t.Fatal("assessment did not reach injected failure", e)
			}
			assertState(before)
			if account.Qualification(before, d.SleeveID) != nil {
				t.Fatal("fixture already promoted")
			}
			removeFailure()
			reused := reuseSequence(before, "reuse-after-assessment", d.OutcomeDeadline)
			assessment, e := pg.AssessForward(ctx, plan.ID, func() time.Time { return d.OutcomeDeadline })
			if e != nil || assessment.Status != "passed" || !assessment.QualificationCredit || assessment.JournalVersion != reused.Version {
				t.Fatal("assessment retry", assessment.Status, e)
			}
			after, e := pg.CappedBook(ctx, r.BookID)
			if e != nil || after.Version != reused.Version+1 || account.Qualification(after, d.SleeveID) == nil {
				t.Fatal("committed assessment missing", e)
			}
			assertState(after)
			retry, e := pg.AssessForward(ctx, plan.ID, func() time.Time { t.Fatal("committed retry read clock"); return time.Time{} })
			if e != nil || !reflect.DeepEqual(retry, assessment) {
				t.Fatal("assessment retry changed result", e)
			}
			assertState(after)
			t.Log("compiled collections and assessment survive warm/cold replay; enclosing failure leaves no custody or grant; different commands reuse both aborted sequences")
		})
	}
}
