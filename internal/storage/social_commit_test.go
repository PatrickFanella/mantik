package storage

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/socialwatch"
	"git.subcult.tv/subculture-collective/mantik/internal/workerplan"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestPostgresContractsSocialCommitAcknowledgements(t *testing.T) {
	p := timerPostgres(t, "socialcommit")
	ctx := context.Background()
	initial, err := p.SocialHistory(ctx)
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
	at := time.Now().UTC()
	now := func() time.Time { return at }
	noClock := func() time.Time { panic("historical social retry read clock") }
	policy := socialPolicy(initial.ReceiverID)
	var policyPin string
	type operation struct {
		name string
		call func(context.Context, *Postgres) (any, error)
	}
	commands := []socialwatch.Command{{Policy: &policy}}
	var retries []operation
	for _, kind := range []string{"watch_policy", "social_capture", "social_research", "copy_capture"} {
		if kind == "social_capture" {
			capture := socialCapture(initial.ReceiverID, policyPin, at)
			capture.RequestResearch = true
			commands = append(commands, socialwatch.Command{Capture: &capture})
		}
		if kind == "social_research" {
			history, e := p.SocialHistory(ctx)
			if e != nil {
				t.Fatal(e)
			}
			source := history.History[len(history.History)-1].Capture
			c := socialwatch.ResearchCommand{ID: "social-selection-commit", ReceiverID: initial.ReceiverID, CaptureID: source.Command.ID, CaptureSHA256: source.SHA256}
			commands = append(commands, socialwatch.Command{Research: &c})
		}
		if kind == "copy_capture" {
			source := socialwatch.Original{Schema: socialwatch.CopyRecordingSchema, Source: socialwatch.SourceRef{Provider: policy.Policy.Provider, AccountID: policy.Policy.AccountID, PostID: "copy-commit"}, Revision: 1, Kind: "post", OccurredAt: at, ObservedAt: at, Text: "Unverified recorded activity", Symbols: []string{"SPY"}, Activity: &socialwatch.Activity{Kind: "observed_fill", State: "reported", SourceSymbol: "SPY.US", Symbol: "SPY", MappingVersion: "assertion-v1", Side: "buy", QuantityUnits: 10, QuantityUnitClaim: "shares", PriceCents: 10000}}
			raw, e := json.Marshal(source)
			if e != nil {
				t.Fatal(e)
			}
			capture := socialwatch.CaptureCommand{ID: "copy-commit", ReceiverID: initial.ReceiverID, WatchID: policy.Policy.ID, PolicySHA256: policyPin, EvidenceClass: "recorded_copy_unqualified", Original: string(raw), OriginalSHA256: workerplan.Digest(raw)}
			commands = append(commands, socialwatch.Command{Capture: &capture})
		}
		command := commands[len(commands)-1]
		t.Run(kind+"_deferred_failure", func(t *testing.T) {
			before, e := p.SocialHistory(ctx)
			if e != nil {
				t.Fatal(e)
			}
			if _, e = p.pool.Exec(ctx, `CREATE FUNCTION reject_social_commit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'social deferred commit rollback'; END $$; CREATE CONSTRAINT TRIGGER reject_social_commit AFTER INSERT OR UPDATE ON mantik_social_watch_journal DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION reject_social_commit()`); e != nil {
				t.Fatal(e)
			}
			got, e := p.ApplySocialCommand(ctx, command, now)
			if _, dropErr := p.pool.Exec(ctx, "DROP TRIGGER reject_social_commit ON mantik_social_watch_journal; DROP FUNCTION reject_social_commit()"); dropErr != nil {
				t.Fatal(dropErr)
			}
			if e == nil || !strings.Contains(e.Error(), "social deferred commit rollback") || !reflect.DeepEqual(got, socialwatch.Receipt{}) {
				t.Errorf("failed COMMIT acknowledged staged social/copy result: %#v, %v", got, e)
			}
			after, e := p.SocialHistory(ctx)
			if e != nil || !reflect.DeepEqual(before, after) {
				t.Fatal("failed COMMIT changed journal", e)
			}
			committed, e := p.ApplySocialCommand(ctx, command, now)
			if e != nil || committed.Duplicate {
				t.Fatal("same identity could not commit after rollback", committed, e)
			}
			if command.Policy != nil {
				policyPin = committed.Entry.Policy.SHA256
			}
			retry, e := p.ApplySocialCommand(ctx, command, noClock)
			if e != nil || !retry.Duplicate || !reflect.DeepEqual(retry.Entry, committed.Entry) {
				t.Fatal("exact retry changed committed source", retry, e)
			}
		})
		retries = append(retries, operation{kind, func(c context.Context, s *Postgres) (any, error) {
			return s.ApplySocialCommand(c, command, noClock)
		}})
	}
	reads := append([]operation{{"history", func(c context.Context, s *Postgres) (any, error) { return s.SocialHistory(c) }}}, retries...)
	for _, op := range reads {
		t.Run(op.name+"_commit_cancel", func(t *testing.T) {
			before, e := p.SocialHistory(ctx)
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
			got, e := op.call(request, &Postgres{pool: pool})
			pool.Close()
			if !tracer.fired.Load() || e == nil || !reflect.ValueOf(got).IsZero() {
				t.Errorf("failed read/retry COMMIT acknowledged source: %#v, %v; fired=%v", got, e, tracer.fired.Load())
			}
			after, e := p.SocialHistory(ctx)
			if e != nil || !reflect.DeepEqual(before, after) {
				t.Fatal("failed read/retry changed journal", e)
			}
		})
	}
	final, err := p.SocialHistory(ctx)
	if err != nil || len(final.History) != 4 {
		t.Fatal(final, err)
	}
	reopened, err := OpenPostgres(ctx, p.pool.Config().ConnString())
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	actual, err := reopened.SocialHistory(ctx)
	if err != nil || !reflect.DeepEqual(final, actual) {
		t.Fatal("reopen changed journal", err)
	}
	after, err := p.Snapshot(ctx)
	if err != nil || !reflect.DeepEqual(operational, after) {
		t.Fatal("social/copy source changed operations", err)
	}
	accountAfter, err := p.PaperAccounts(ctx)
	if err != nil || !reflect.DeepEqual(accounts, accountAfter) {
		t.Fatal("social/copy source changed capital", err)
	}
}
