package storage

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/feedcollector"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Cancel only when pgx is about to commit a real read/retry transaction. This
// exercises the acknowledgement boundary after all source reads have succeeded.
type cancelCollectorCommit struct {
	cancel context.CancelFunc
	fired  atomic.Bool
}

func (c *cancelCollectorCommit) TraceQueryStart(ctx context.Context, _ *pgx.Conn, data pgx.TraceQueryStartData) context.Context {
	if strings.EqualFold(strings.TrimSpace(data.SQL), "commit") {
		c.fired.Store(true)
		c.cancel()
	}
	return ctx
}
func (*cancelCollectorCommit) TraceQueryEnd(context.Context, *pgx.Conn, pgx.TraceQueryEndData) {}

func TestPostgresContractsCollectorCommitAcknowledgements(t *testing.T) {
	p := timerPostgres(t, "collectorcommit")
	ctx := context.Background()
	at := time.Date(2026, 10, 15, 13, 30, 0, 0, time.UTC)
	now := func() time.Time { return at }
	initial, err := p.FeedCollectors(ctx)
	if err != nil {
		t.Fatal(err)
	}
	owner := initial.ReceiverID
	reg := feedcollector.Registration{ID: "commit-feed", Provider: "alpaca", Feed: "iex", Symbol: "SPY", EvidenceClass: "prospective_market_unqualified", JournalID: "journal-one", AccessRef: "access-one", LeaseSeconds: 300}
	for _, c := range []feedcollector.Command{{RequestID: "commit-register", Operation: "register", RegistrationID: reg.ID, Registration: &reg}, {RequestID: "commit-claim", Operation: "claim", RegistrationID: reg.ID, OwnerID: "worker"}} {
		if _, err = p.FeedCollectorCommand(ctx, owner, c, now); err != nil {
			t.Fatal(err)
		}
	}
	at = at.Add(time.Second)
	fence := feedcollector.Fence{RegistrationID: reg.ID, OwnerID: "worker", Generation: 1}
	renew := feedcollector.Command{RequestID: "commit-renew", Operation: "renew", RegistrationID: reg.ID, OwnerID: "worker", Generation: 1}
	input := collectorInput(at, "commit-input")
	raw := json.RawMessage(`{"fixture":"collector-commit-only"}`)
	check := func(FeedEvidenceSnapshot, time.Time, *FeedEvidenceRecord) error { return nil }
	type operation struct {
		name string
		call func(context.Context, *Postgres) (any, error)
	}
	writes := []operation{
		{"ownership", func(c context.Context, s *Postgres) (any, error) { return s.FeedCollectorCommand(c, owner, renew, now) }},
		{"input_admission", func(c context.Context, s *Postgres) (any, error) {
			return s.SubmitCollectorResearch(c, owner, fence, input, now)
		}},
		{"raw_publication", func(c context.Context, s *Postgres) (any, error) {
			return s.PublishFeedEvidence(c, owner, reg.ID, "commit-raw", fence, raw, now, check)
		}},
	}
	for _, op := range writes {
		t.Run(op.name+"_deferred_failure", func(t *testing.T) {
			before, e := p.FeedEvidence(ctx)
			if e != nil {
				t.Fatal(e)
			}
			if _, e = p.pool.Exec(ctx, `CREATE FUNCTION reject_collector_commit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'collector deferred commit rollback'; END $$; CREATE CONSTRAINT TRIGGER reject_collector_commit AFTER UPDATE ON mantik_feed_collectors DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION reject_collector_commit()`); e != nil {
				t.Fatal(e)
			}
			got, e := op.call(ctx, p)
			// Drop the fixture before reporting an assertion, so subsequent operations
			// independently exercise the same commit boundary.
			if _, dropErr := p.pool.Exec(ctx, "DROP TRIGGER reject_collector_commit ON mantik_feed_collectors; DROP FUNCTION reject_collector_commit()"); dropErr != nil {
				t.Fatal(dropErr)
			}
			if e == nil || !strings.Contains(e.Error(), "collector deferred commit rollback") || !reflect.ValueOf(got).IsZero() {
				t.Errorf("COMMIT failure acknowledged staged source result: %#v, %v", got, e)
			}
			after, e := p.FeedEvidence(ctx)
			if e != nil || !reflect.DeepEqual(before, after) {
				t.Fatal("failed COMMIT changed source owner", e)
			}
			got, e = op.call(ctx, p)
			if e != nil || reflect.ValueOf(got).IsZero() {
				t.Fatal("same identity could not commit after rollback", got, e)
			}
			retry, e := op.call(ctx, p)
			if e != nil || reflect.ValueOf(retry).IsZero() {
				t.Fatal("exact retry lost committed result", retry, e)
			}
		})
	}
	reads := append([]operation{
		{"collector_snapshot", func(c context.Context, s *Postgres) (any, error) { return s.FeedCollectors(c) }},
		{"evidence_snapshot", func(c context.Context, s *Postgres) (any, error) { return s.FeedEvidence(c) }},
	}, writes...)
	for _, op := range reads {
		t.Run(op.name+"_commit_cancel", func(t *testing.T) {
			before, e := p.FeedEvidence(ctx)
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
			isolated := &Postgres{pool: pool}
			got, e := op.call(request, isolated)
			pool.Close()
			if !tracer.fired.Load() || e == nil || !reflect.ValueOf(got).IsZero() {
				t.Errorf("failed read/retry COMMIT acknowledged source result: %#v, %v; fired=%v", got, e, tracer.fired.Load())
			}
			after, e := p.FeedEvidence(ctx)
			if e != nil || !reflect.DeepEqual(before, after) {
				t.Fatal("failed read/retry changed owner", e)
			}
		})
	}
	reopened, e := OpenPostgres(ctx, p.pool.Config().ConnString())
	if e != nil {
		t.Fatal(e)
	}
	defer reopened.Close()
	before, e := p.FeedEvidence(ctx)
	if e != nil {
		t.Fatal(e)
	}
	after, e := reopened.FeedEvidence(ctx)
	if e != nil || !reflect.DeepEqual(before, after) {
		t.Fatal("reopen changed committed source state", e)
	}
}
