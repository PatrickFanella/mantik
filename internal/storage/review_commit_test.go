package storage

import (
	"context"
	"reflect"
	"strings"
	"testing"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/news"
	"git.subcult.tv/subculture-collective/mantik/internal/review"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestPostgresContractsReviewCommitAcknowledgements(t *testing.T) {
	p := timerPostgres(t, "reviewcommit")
	ctx := context.Background()
	recording := news.Fixture()
	at := recording.ReceivedAt.Add(time.Hour)
	now := func() time.Time { return at }
	noClock := func() time.Time { panic("exact review retry read clock") }
	catalog, err := p.NewsCases(ctx)
	if err != nil {
		t.Fatal(err)
	}
	owner := catalog.ReceiverID
	admitted, err := p.AdmitNews(ctx, owner, recording, now)
	if err != nil {
		t.Fatal(err)
	}
	caseID := admitted.Matches[0].CaseID
	newsBefore, err := p.NewsCases(ctx)
	if err != nil {
		t.Fatal(err)
	}
	operational, err := p.Snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	accounts, err := p.PaperAccounts(ctx)
	if err != nil {
		t.Fatal(err)
	}
	type operation struct {
		name string
		call func(context.Context, *Postgres, func() time.Time) (review.View, error)
	}
	retries := []operation{}
	for _, kind := range []string{"start", "complete"} {
		for _, fault := range []string{"deferred", "cancel"} {
			id := kind + "-" + fault
			op := operation{name: id, call: func(c context.Context, s *Postgres, clock func() time.Time) (review.View, error) {
				return s.StartReview(c, owner, id, caseID, 1, clock)
			}}
			if kind == "complete" {
				pending, e := p.StartReview(ctx, owner, id, caseID, 1, now)
				if e != nil {
					t.Fatal(e)
				}
				steps := reviewSteps(pending.Attempt.Pin.Group.Claims[0].Claim.Key)
				op.call = func(c context.Context, s *Postgres, clock func() time.Time) (review.View, error) {
					return s.CompleteReview(c, owner, id, steps, clock)
				}
			}
			t.Run(id, func(t *testing.T) {
				before, e := p.ReviewHistory(ctx, caseID)
				if e != nil {
					t.Fatal(e)
				}
				request, cancel := context.WithCancel(ctx)
				defer cancel()
				target := p
				var pool *pgxpool.Pool
				var tracer *cancelCollectorCommit
				if fault == "deferred" {
					_, e = p.pool.Exec(ctx, `CREATE FUNCTION reject_review_commit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'review deferred commit rollback'; END $$; CREATE CONSTRAINT TRIGGER reject_review_commit AFTER INSERT OR UPDATE ON mantik_news_reviews DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION reject_review_commit()`)
					if e != nil {
						t.Fatal(e)
					}
				} else {
					tracer = &cancelCollectorCommit{cancel: cancel}
					config := p.pool.Config()
					config.ConnConfig.Tracer = tracer
					pool, e = pgxpool.NewWithConfig(ctx, config)
					if e != nil {
						t.Fatal(e)
					}
					target = &Postgres{pool: pool}
				}
				got, e := op.call(request, target, now)
				if pool != nil {
					pool.Close()
				}
				if fault == "deferred" {
					if _, dropErr := p.pool.Exec(ctx, "DROP TRIGGER reject_review_commit ON mantik_news_reviews; DROP FUNCTION reject_review_commit()"); dropErr != nil {
						t.Fatal(dropErr)
					}
					if e == nil || !strings.Contains(e.Error(), "review deferred commit rollback") {
						t.Errorf("deferred COMMIT not reached: %v", e)
					}
				} else if !tracer.fired.Load() {
					t.Error("COMMIT cancellation not reached")
				}
				if e == nil || !reflect.DeepEqual(got, review.View{}) {
					t.Errorf("failed COMMIT acknowledged staged review: %#v, %v", got, e)
				}
				after, e := p.ReviewHistory(ctx, caseID)
				if e != nil || !reflect.DeepEqual(after, before) {
					t.Fatal("failed COMMIT changed review history", e)
				}
				committed, e := op.call(ctx, p, now)
				if e != nil {
					t.Fatal("same identity could not succeed after rollback", e)
				}
				want := "pending"
				if kind == "complete" {
					want = "completed"
				}
				if committed.Attempt.Status != want {
					t.Fatal(committed.Attempt.Status)
				}
				retry, e := op.call(ctx, p, noClock)
				if e != nil || !reflect.DeepEqual(committed, retry) {
					t.Fatal("clock-free exact retry changed review", e)
				}
			})
			retries = append(retries, op)
		}
	}
	for _, op := range retries {
		t.Run(op.name+"_retry_commit_cancel", func(t *testing.T) {
			before, e := p.ReviewHistory(ctx, caseID)
			if e != nil {
				t.Fatal(e)
			}
			request, cancel := context.WithCancel(ctx)
			defer cancel()
			tracer := &cancelCollectorCommit{cancel: cancel}
			config := p.pool.Config()
			config.ConnConfig.Tracer = tracer
			pool, e := pgxpool.NewWithConfig(ctx, config)
			if e != nil {
				t.Fatal(e)
			}
			got, e := op.call(request, &Postgres{pool: pool}, noClock)
			pool.Close()
			if !tracer.fired.Load() || e == nil || !reflect.DeepEqual(got, review.View{}) {
				t.Errorf("failed exact-retry COMMIT acknowledged review: %#v, %v; fired=%v", got, e, tracer.fired.Load())
			}
			after, e := p.ReviewHistory(ctx, caseID)
			if e != nil || !reflect.DeepEqual(after, before) {
				t.Fatal("failed retry changed history", e)
			}
		})
	}
	final, err := p.ReviewHistory(ctx, caseID)
	if err != nil || final.AttemptCount != 4 {
		t.Fatal(final, err)
	}
	reopened, err := OpenPostgres(ctx, p.pool.Config().ConnString())
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	actual, err := reopened.ReviewHistory(ctx, caseID)
	if err != nil || !reflect.DeepEqual(actual, final) {
		t.Fatal("reopen changed review history", err)
	}
	newsAfter, err := p.NewsCases(ctx)
	if err != nil || !reflect.DeepEqual(newsAfter, newsBefore) {
		t.Fatal("review changed source news", err)
	}
	after, err := p.Snapshot(ctx)
	if err != nil || !reflect.DeepEqual(after, operational) {
		t.Fatal("review changed operations", err)
	}
	accountAfter, err := p.PaperAccounts(ctx)
	if err != nil || !reflect.DeepEqual(accountAfter, accounts) {
		t.Fatal("review changed capital", err)
	}
}
