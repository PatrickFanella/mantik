package storage

import (
	"context"
	"encoding/json"
	"git.subcult.tv/subculture-collective/mantik/internal/socialwatch"
	"git.subcult.tv/subculture-collective/mantik/internal/workerplan"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestMemorySocialResearchRequests(t *testing.T) { socialResearchContracts(t, NewMemory()) }
func TestPostgresContractsSocialResearchRequests(t *testing.T) {
	p := timerPostgres(t, "socialresearch")
	socialResearchContracts(t, p)
	socialResearchMigration(t, p)
}
func socialResearchContracts(t *testing.T, s socialBackend) {
	ctx := context.Background()
	initial, err := s.SocialHistory(ctx)
	if err != nil {
		t.Fatal(err)
	}
	at := time.Now().UTC()
	now := func() time.Time { return at }
	noClock := func() time.Time { panic("research retry clock") }
	before, err := s.Snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	accounts, err := s.PaperAccounts(ctx)
	if err != nil {
		t.Fatal(err)
	}
	policy := socialPolicy(initial.ReceiverID)
	registered, err := s.ApplySocialCommand(ctx, socialwatch.Command{Policy: &policy}, now)
	if err != nil {
		t.Fatal(err)
	}
	c := socialCapture(initial.ReceiverID, registered.Entry.Policy.SHA256, at)
	c.RequestResearch = true
	var calls atomic.Int32
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			if _, e := s.ApplySocialCommand(ctx, socialwatch.Command{Capture: &c}, func() time.Time { calls.Add(1); return at }); e != nil {
				t.Error(e)
			}
		})
	}
	wg.Wait()
	if calls.Load() != 1 {
		t.Fatal("concurrent research issued twice", calls.Load())
	}
	retry, err := s.ApplySocialCommand(ctx, socialwatch.Command{Capture: &c}, noClock)
	if err != nil || !retry.Duplicate || retry.Entry.Capture.ResearchRequest == nil {
		t.Fatal(retry, err)
	}
	request := *retry.Entry.Capture.ResearchRequest
	unsigned := request
	unsigned.SHA256 = ""
	if workerplan.Hash(unsigned) != request.SHA256 || request.ReceiverID != initial.ReceiverID || request.PolicySHA256 != c.PolicySHA256 || request.CaptureID != c.ID || request.OriginalSHA256 != c.OriginalSHA256 || request.Status != "pending_research" || request.DispatchAvailable || request.AdmissionAvailable || request.ExecutionAvailable || request.QualificationCredit || request.IndependentEvidenceCredit {
		t.Fatal("unbound or authoritative request", request)
	}
	all, err := s.SocialHistory(ctx)
	if err != nil || len(all.ResearchRequests) != 1 {
		t.Fatal(all, err)
	}
	// Different capture/source names and repeated prose do not multiply research.
	original, _ := c.Decode()
	original.Source.PostID = "repeat"
	original.Text = " SPY   SOURCE ASSERTION "
	raw, _ := json.Marshal(original)
	other := c
	other.ID = "repeated-claim"
	other.Original = string(raw)
	other.OriginalSHA256 = workerplan.Digest(raw)
	repeated, err := s.ApplySocialCommand(ctx, socialwatch.Command{Capture: &other}, now)
	if err != nil || !repeated.Entry.Capture.ResearchRequestDuplicate || !reflect.DeepEqual(*repeated.Entry.Capture.ResearchRequest, request) {
		t.Fatal("claim multiplied research", repeated, err)
	}
	full, err := s.SocialHistory(ctx)
	if err != nil || len(full.ResearchRequests) != 1 {
		t.Fatal(full, err)
	}
	request.Status = "caller mutation"
	full.ResearchRequests[0].Status = "caller mutation"
	actual, err := s.SocialHistory(ctx)
	if err != nil || actual.ResearchRequests[0].Status != "pending_research" {
		t.Fatal("returned request aliases owner", err)
	}
	if pg, ok := s.(*Postgres); ok {
		reopened, e := OpenPostgres(ctx, pg.pool.Config().ConnString())
		if e != nil {
			t.Fatal(e)
		}
		defer reopened.Close()
		got, e := reopened.SocialHistory(ctx)
		if e != nil || !reflect.DeepEqual(got, actual) {
			t.Fatal("reopen changed research", e)
		}
	}
	// Disabling a watch cannot admit a new request. Prior requests remain facts.
	policy.ID = "disable"
	policy.PreviousSHA256 = registered.Entry.Policy.SHA256
	policy.Policy.Enabled = false
	disabled, err := s.ApplySocialCommand(ctx, socialwatch.Command{Policy: &policy}, now)
	if err != nil {
		t.Fatal(err)
	}
	other.ID = "disabled-request"
	other.PolicySHA256 = disabled.Entry.Policy.SHA256
	original.Source.PostID = "disabled"
	raw, _ = json.Marshal(original)
	other.Original = string(raw)
	other.OriginalSHA256 = workerplan.Digest(raw)
	prior, err := s.SocialHistory(ctx)
	if err != nil {
		t.Fatal(err)
	}
	failed, e := s.ApplySocialCommand(ctx, socialwatch.Command{Capture: &other}, now)
	if e == nil || !reflect.DeepEqual(failed, socialwatch.Receipt{}) {
		t.Fatal("disabled watch queued research", failed, e)
	}
	after, e := s.SocialHistory(ctx)
	if e != nil || !reflect.DeepEqual(prior, after) {
		t.Fatal("rejected request changed journal", e)
	}
	state, _ := s.Snapshot(ctx)
	capital, _ := s.PaperAccounts(ctx)
	if !reflect.DeepEqual(before, state) || !reflect.DeepEqual(accounts, capital) {
		t.Fatal("research request changed operational/financial state")
	}
}

func socialResearchMigration(t *testing.T, p *Postgres) {
	t.Helper()
	ctx := context.Background()
	var before, after, marker, hash string
	query := "SELECT coalesce(jsonb_agg(payload ORDER BY singleton),'[]'::jsonb)::text FROM mantik_social_watch_journal"
	if err := p.pool.QueryRow(ctx, query).Scan(&before); err != nil {
		t.Fatal(err)
	}
	if _, err := p.pool.Exec(ctx, `DELETE FROM mantik_schema WHERE version>=67; COMMENT ON TABLE mantik_social_watch_journal IS 'schema66 social research fixture'; CREATE FUNCTION reject_social_research_migration() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.version=67 THEN RAISE EXCEPTION 'social research migration rollback'; END IF; RETURN NEW; END $$; CREATE TRIGGER reject_social_research_migration BEFORE INSERT ON mantik_schema FOR EACH ROW EXECUTE FUNCTION reject_social_research_migration()`); err != nil {
		t.Fatal(err)
	}
	if err := p.migrate(ctx); err == nil {
		t.Fatal("migration failure ignored")
	}
	if err := p.pool.QueryRow(ctx, "SELECT obj_description('mantik_social_watch_journal'::regclass)").Scan(&marker); err != nil || marker != "schema66 social research fixture" {
		t.Fatal("migration comment escaped rollback", marker, err)
	}
	if _, err := p.pool.Exec(ctx, "DROP TRIGGER reject_social_research_migration ON mantik_schema; DROP FUNCTION reject_social_research_migration()"); err != nil {
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
	if err := p.pool.QueryRow(ctx, "SELECT sha256 FROM mantik_schema WHERE version=67").Scan(&hash); err != nil {
		t.Fatal(err)
	}
	if _, err := p.pool.Exec(ctx, "UPDATE mantik_schema SET sha256='damaged' WHERE version=67"); err != nil {
		t.Fatal(err)
	}
	if err := p.migrate(ctx); err == nil {
		t.Fatal("social research migration checksum corruption accepted")
	}
	if _, err := p.pool.Exec(ctx, "UPDATE mantik_schema SET sha256=$1 WHERE version=67", hash); err != nil {
		t.Fatal(err)
	}
}
