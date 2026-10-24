package storage

import (
	"context"
	"encoding/json"
	"git.subcult.tv/subculture-collective/mantik/internal/paperaccount"
	"git.subcult.tv/subculture-collective/mantik/internal/socialwatch"
	"git.subcult.tv/subculture-collective/mantik/internal/workerplan"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type socialBackend interface {
	Store
	SocialWatchStore
	NewsStore
	PaperAccountStore
}

func socialPolicy(owner string) socialwatch.PolicyCommand {
	return socialwatch.PolicyCommand{ID: "create-watch", ReceiverID: owner, Policy: socialwatch.Policy{ID: "watch", Provider: "recorded", AccountID: "account-123", Symbols: []string{"SPY"}, Behavior: "research", MaxObservationDelaySeconds: 60, Enabled: true}}
}
func socialCapture(owner, pin string, at time.Time) socialwatch.CaptureCommand {
	original := socialwatch.Original{Schema: "mantik-social-recording-v1", Source: socialwatch.SourceRef{Provider: "recorded", AccountID: "account-123", PostID: "post-1"}, Revision: 1, Kind: "post", OccurredAt: at, ObservedAt: at, Text: "SPY source assertion", Symbols: []string{"SPY"}}
	raw, _ := json.Marshal(original)
	return socialwatch.CaptureCommand{ID: "capture", ReceiverID: owner, WatchID: "watch", PolicySHA256: pin, EvidenceClass: "recorded_social_unqualified", Original: string(raw), OriginalSHA256: workerplan.Digest(raw)}
}
func TestMemoryOwnedSocialJournal(t *testing.T) { socialContracts(t, NewMemory()) }
func TestPostgresContractsOwnedSocialJournal(t *testing.T) {
	socialContracts(t, timerPostgres(t, "socialjournal"))
}
func socialContracts(t *testing.T, s socialBackend) {
	ctx := context.Background()
	news, err := s.NewsCases(ctx)
	if err != nil {
		t.Fatal(err)
	}
	owner := news.ReceiverID
	at := time.Now().UTC()
	before, _ := s.Snapshot(ctx)
	accounts, _ := s.PaperAccounts(ctx)
	c := socialPolicy(owner)
	var clocks atomic.Int32
	clock := func() time.Time { clocks.Add(1); return at }
	noClock := func() time.Time { panic("retry clock") }
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			if _, err := s.ApplySocialCommand(ctx, socialwatch.Command{Policy: &c}, clock); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	if clocks.Load() != 1 {
		t.Fatal(clocks.Load())
	}
	first, err := s.ApplySocialCommand(ctx, socialwatch.Command{Policy: &c}, noClock)
	if err != nil || !first.Duplicate {
		t.Fatal(first, err)
	}
	cap := socialCapture(owner, first.Entry.Policy.SHA256, at)
	for range 8 {
		wg.Go(func() {
			if _, err := s.ApplySocialCommand(ctx, socialwatch.Command{Capture: &cap}, clock); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	if clocks.Load() != 2 {
		t.Fatal(clocks.Load())
	}
	capture, err := s.ApplySocialCommand(ctx, socialwatch.Command{Capture: &cap}, noClock)
	if err != nil || !capture.Duplicate || capture.Entry.Capture.PolicyStatus != "dispatch_unbound" {
		t.Fatal(capture, err)
	}
	catalog, err := s.SocialHistory(ctx)
	if err != nil || len(catalog.History) != 2 || len(catalog.Watches) != 1 {
		t.Fatal(catalog, err)
	}
	capture.Entry.Capture.Source.Text = "mutated"
	again, err := s.SocialHistory(ctx)
	if err != nil || !reflect.DeepEqual(again, catalog) {
		t.Fatal("alias mutation", err)
	}
	after, _ := s.Snapshot(ctx)
	accountAfter, _ := s.PaperAccounts(ctx)
	if !reflect.DeepEqual(before, after) || !reflect.DeepEqual(accounts, accountAfter) {
		t.Fatal("social records changed operations/capital")
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := s.ApplySocialCommand(canceled, socialwatch.Command{Policy: &c}, noClock); err != context.Canceled {
		t.Fatal(err)
	}
	conflicting := c
	conflicting.Policy.Enabled = false
	if _, err := s.ApplySocialCommand(ctx, socialwatch.Command{Policy: &conflicting}, noClock); err != socialwatch.ErrConflict {
		t.Fatal(err)
	}
	var original socialwatch.Journal
	if m, ok := s.(*Memory); ok {
		original = clone(*m.socialJournal)
	} else {
		var raw []byte
		if err := s.(*Postgres).pool.QueryRow(ctx, "SELECT payload FROM mantik_social_watch_journal WHERE singleton").Scan(&raw); err != nil {
			t.Fatal(err)
		}
		if json.Unmarshal(raw, &original) != nil {
			t.Fatal("decode")
		}
	}
	set := func(j socialwatch.Journal) {
		t.Helper()
		if m, ok := s.(*Memory); ok {
			copy := clone(j)
			m.socialJournal = &copy
		} else {
			raw, _ := json.Marshal(j)
			if _, err := s.(*Postgres).pool.Exec(ctx, "UPDATE mantik_social_watch_journal SET payload=$1 WHERE singleton", raw); err != nil {
				t.Fatal(err)
			}
		}
	}
	damaged := clone(original)
	last := &damaged.Entries[1]
	last.Capture.ExecutionAvailable = true
	last.Capture.SHA256 = ""
	last.Capture.SHA256 = workerplan.Hash(last.Capture)
	last.SHA256 = ""
	last.SHA256 = workerplan.Hash(last)
	set(damaged)
	if _, err := s.SocialHistory(ctx); err == nil {
		t.Fatal("rehashed execution forgery")
	}
	if _, err := s.ApplySocialCommand(ctx, socialwatch.Command{Policy: &c}, noClock); err == nil {
		t.Fatal("retry hid corruption")
	}
	if _, err := s.ApplySocialCommand(ctx, socialwatch.Command{Capture: &cap}, noClock); err == nil {
		t.Fatal("capture retry hid corruption")
	}
	set(original)
	if pg, ok := s.(*Postgres); ok {
		cfg := pg.pool.Config().ConnString()
		pg.Close()
		reopened, err := OpenPostgres(ctx, cfg)
		if err != nil {
			t.Fatal(err)
		}
		defer reopened.Close()
		got, err := reopened.SocialHistory(ctx)
		if err != nil || !reflect.DeepEqual(got, catalog) {
			t.Fatal("reopen", err)
		}
		retry, err := reopened.ApplySocialCommand(ctx, socialwatch.Command{Capture: &cap}, noClock)
		if err != nil || !retry.Duplicate {
			t.Fatal(retry, err)
		}
	}
}
func TestPostgresContractsSocialUpgradeRollback(t *testing.T) {
	ctx := context.Background()
	s := timerPostgres(t, "socialupgrade")
	news, err := s.NewsCases(ctx)
	if err != nil {
		t.Fatal(err)
	}
	owner := news.ReceiverID
	at := time.Now().UTC()
	clock := func() time.Time { return at }
	if _, err := s.CreatePaperAccount(ctx, owner, paperaccount.Config{ID: "preserved", Currency: "USD", OpeningCashCents: 100000, CapitalLimitCents: 100000}, clock); err != nil {
		t.Fatal(err)
	}
	before, _ := s.Snapshot(ctx)
	accounts, _ := s.PaperAccounts(ctx)
	cfg := s.pool.Config().ConnString()
	if _, err := s.pool.Exec(ctx, "DROP TABLE mantik_paper_evaluation_declarations; DROP TABLE mantik_decision_reviews; DROP TABLE mantik_social_watch_journal; DROP TABLE IF EXISTS mantik_paper_evaluation_assessments; DELETE FROM mantik_schema WHERE version>=41"); err != nil {
		t.Fatal(err)
	}
	s.Close()
	up, err := OpenPostgres(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer up.Close()
	after, _ := up.Snapshot(ctx)
	accountAfter, _ := up.PaperAccounts(ctx)
	if !reflect.DeepEqual(before, after) || !reflect.DeepEqual(accounts, accountAfter) {
		t.Fatal("upgrade changed capital/operations")
	}
	if _, err := up.pool.Exec(ctx, `CREATE FUNCTION reject_social() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'test rollback'; END $$; CREATE TRIGGER reject_social BEFORE INSERT OR UPDATE ON mantik_social_watch_journal FOR EACH ROW EXECUTE FUNCTION reject_social()`); err != nil {
		t.Fatal(err)
	}
	c := socialPolicy(owner)
	if _, err := up.ApplySocialCommand(ctx, socialwatch.Command{Policy: &c}, clock); err == nil {
		t.Fatal("failed insert accepted")
	}
	empty, err := up.SocialHistory(ctx)
	if err != nil || len(empty.History) != 0 {
		t.Fatal(empty, err)
	}
	if _, err := up.pool.Exec(ctx, "ALTER TABLE mantik_social_watch_journal DISABLE TRIGGER reject_social"); err != nil {
		t.Fatal(err)
	}
	first, err := up.ApplySocialCommand(ctx, socialwatch.Command{Policy: &c}, clock)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := up.pool.Exec(ctx, "ALTER TABLE mantik_social_watch_journal ENABLE TRIGGER reject_social"); err != nil {
		t.Fatal(err)
	}
	cap := socialCapture(owner, first.Entry.Policy.SHA256, at)
	if _, err := up.ApplySocialCommand(ctx, socialwatch.Command{Capture: &cap}, clock); err == nil {
		t.Fatal("failed update accepted")
	}
	onlyPolicy, err := up.SocialHistory(ctx)
	if err != nil || len(onlyPolicy.History) != 1 {
		t.Fatal(onlyPolicy, err)
	}
	if _, err := up.pool.Exec(ctx, "DROP TRIGGER reject_social ON mantik_social_watch_journal; DROP FUNCTION reject_social()"); err != nil {
		t.Fatal(err)
	}
	if _, err := up.ApplySocialCommand(ctx, socialwatch.Command{Capture: &cap}, clock); err != nil {
		t.Fatal(err)
	}
	if _, err := up.pool.Exec(ctx, "UPDATE mantik_schema SET sha256='changed' WHERE version=41"); err != nil {
		t.Fatal(err)
	}
	if broken, err := OpenPostgres(ctx, cfg); err == nil {
		broken.Close()
		t.Fatal("migration checksum changed")
	}
}
