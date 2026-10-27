package storage

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/alpacarecord"
	"git.subcult.tv/subculture-collective/mantik/internal/workerplan"
)

func corporateSourceFixture(owner string) CorporateActionSourceCommand {
	raw := `{"corporate_actions":{"cash_dividends":[{"id":"original-id","rate":0.123456789,"unknown":{"retained":true}}]},"next_page_token":"next+cursor"}`
	return CorporateActionSourceCommand{ID: "source-one", ReceiverID: owner, Recording: alpacarecord.Recording{Schema: alpacarecord.RecordingSchema, Currency: "USD", CaptureID: "corporate-one", Revision: 1, Capability: "corporate_actions", Feed: alpacarecord.CorporateActionFeed, Symbols: []string{"SPY"}, Start: "2026-10-14", End: "2026-10-14", ReceivedAt: time.Date(2026, 10, 15, 14, 0, 0, 0, time.UTC), EvidenceClass: "synthetic_unqualified", AccessRef: "manufactured-access", RawRef: "journal:manufactured:capture-corporate-one-r1.json", Original: raw, SHA256: alpacarecord.Digest(raw), MaxQuoteAgeSeconds: 60}}
}

type corporateSourceBackend interface {
	CorporateActionSourceStore
	ResearchReceiverStore
	Store
}

func TestMemoryCorporateActionSources(t *testing.T) { corporateSourceContracts(t, NewMemory()) }
func TestPostgresContractsCorporateActionSources(t *testing.T) {
	p := timerPostgres(t, "corporatesources")
	corporateSourceContracts(t, p)
	corporateSourceMigration(t, p)
}
func corporateSourceContracts(t *testing.T, s corporateSourceBackend) {
	t.Helper()
	ctx := context.Background()
	owner, err := s.ResearchSnapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	before, err := s.Snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	c := corporateSourceFixture(owner.ID)
	at := c.Recording.ReceivedAt.Add(time.Hour)
	clock := func() time.Time { return at }
	r, err := s.RetainCorporateActionSource(ctx, c, clock)
	if err != nil || r.Duplicate || r.Record.Sequence != 1 || !r.Record.RetainedAt.Equal(at) || r.Record.Command.Recording.Original != c.Recording.Original {
		t.Fatal(r, err)
	}
	noClock := func() time.Time { t.Fatal("immutable retry consulted clock"); return time.Time{} }
	retry, err := s.RetainCorporateActionSource(ctx, c, noClock)
	if err != nil || !retry.Duplicate || !reflect.DeepEqual(retry.Record, r.Record) {
		t.Fatal(retry, err)
	}
	changed := clone(c)
	changed.Recording.Original = `{"corporate_actions":{},"next_page_token":null}`
	changed.Recording.SHA256 = alpacarecord.Digest(changed.Recording.Original)
	if _, err = s.RetainCorporateActionSource(ctx, changed, noClock); !errors.Is(err, ErrCorporateActionSourceConflict) {
		t.Fatal(err)
	}
	alias := clone(c)
	alias.ID = "aliased-source"
	if _, err = s.RetainCorporateActionSource(ctx, alias, noClock); !errors.Is(err, ErrCorporateActionSourceConflict) {
		t.Fatal(err)
	}
	wrong := clone(c)
	wrong.ReceiverID = "receiver-abcdefghijklmnop"
	if _, err = s.RetainCorporateActionSource(ctx, wrong, noClock); !errors.Is(err, ErrReceiverMismatch) {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func(*CorporateActionSourceCommand){
		"future":      func(c *CorporateActionSourceCommand) { c.Recording.ReceivedAt = at.Add(time.Second) },
		"unsupported": func(c *CorporateActionSourceCommand) { c.Recording.Capability = "latest_quotes" },
		"hash":        func(c *CorporateActionSourceCommand) { c.Recording.SHA256 = strings.Repeat("f", 64) },
		"duplicate_json": func(c *CorporateActionSourceCommand) {
			c.Recording.Original = `{"corporate_actions":{},"corporate_actions":{},"next_page_token":null}`
			c.Recording.SHA256 = alpacarecord.Digest(c.Recording.Original)
		},
	} {
		t.Run(name, func(t *testing.T) {
			bad := clone(c)
			bad.ID = "bad-" + name
			bad.Recording.RawRef = "artifact:" + name
			mutate(&bad)
			if _, err := s.RetainCorporateActionSource(ctx, bad, clock); !errors.Is(err, ErrCorporateActionSourceInvalid) {
				t.Fatal(err)
			}
		})
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err = s.RetainCorporateActionSource(cancelled, c, clock); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	backward := clone(c)
	backward.ID = "backward-clock"
	backward.Recording.RawRef = "artifact:backward-clock"
	if _, err = s.RetainCorporateActionSource(ctx, backward, func() time.Time { return at.Add(-time.Nanosecond) }); !errors.Is(err, ErrCorporateActionSourceInvalid) {
		t.Fatal("source journal accepted a backward retention clock", err)
	}
	// Concurrent identical writers create one new immutable revision only.
	c2 := clone(c)
	c2.ID = "source-two"
	c2.Recording.Revision = 2
	c2.Recording.RawRef = "journal:manufactured:capture-corporate-one-r2.json"
	c2.Recording.Original = `{"corporate_actions":{},"next_page_token":null}`
	c2.Recording.SHA256 = alpacarecord.Digest(c2.Recording.Original)
	var wg sync.WaitGroup
	for range 4 {
		wg.Go(func() {
			if _, err := s.RetainCorporateActionSource(ctx, c2, clock); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	cat, err := s.CorporateActionSources(ctx)
	if err != nil || len(cat.Records) != 2 || cat.Records[1].PreviousSHA256 != r.Record.SHA256 {
		t.Fatal(cat, err)
	}
	if p, ok := s.(*Postgres); ok {
		reopened, err := OpenPostgres(ctx, p.pool.Config().ConnString())
		if err != nil {
			t.Fatal(err)
		}
		got, err := reopened.CorporateActionSources(ctx)
		reopened.Close()
		if err != nil || !reflect.DeepEqual(got, cat) {
			t.Fatal("reopen changed original source custody", err)
		}
	}
	for _, record := range cat.Records {
		assertCorporateSourceFlags(t, record)
	}
	cat.Records[0].Command.Recording.Symbols[0] = "MUTATED"
	again, err := s.CorporateActionSources(ctx)
	if err != nil || again.Records[0].Command.Recording.Symbols[0] != "SPY" {
		t.Fatal("readback mutated owner", err)
	}
	state, err := s.Snapshot(ctx)
	if err != nil || !reflect.DeepEqual(state, before) {
		t.Fatal("source custody changed operational state", err)
	}
}
func assertCorporateSourceFlags(t *testing.T, r CorporateActionSourceRecord) {
	t.Helper()
	hash := r.SHA256
	r.SHA256 = ""
	if r.ProviderAttested || r.CoverageComplete || r.EconomicApplicationReady || r.AdmissionAvailable || r.ExecutionAvailable || r.QualificationCredit || hash != workerplan.Hash(r) {
		t.Fatal("source custody acquired unsupported authority", r)
	}
}
func TestCorporateActionSourceReplayRejectsRehashedGrantsAndCapacity(t *testing.T) {
	ctx := context.Background()
	m := NewMemory()
	c := corporateSourceFixture(m.receiverID)
	at := c.Recording.ReceivedAt
	now := func() time.Time { return at }
	for i := range MaxCorporateActionSources {
		v := clone(c)
		v.ID = fmt.Sprintf("source-%02d", i)
		v.Recording.RawRef = "artifact:" + v.ID
		if _, err := m.RetainCorporateActionSource(ctx, v, now); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := m.RetainCorporateActionSource(ctx, c, now); !errors.Is(err, ErrCorporateActionSourceCapacity) {
		t.Fatal(err)
	}
	if retry, err := m.RetainCorporateActionSource(ctx, m.corporateActionSources[0].Command, func() time.Time { t.Fatal("full journal retry read clock"); return time.Time{} }); err != nil || !retry.Duplicate {
		t.Fatal("full journal lost an immutable retry", err)
	}
	original := clone(m.corporateActionSources)
	for _, grant := range []string{"provider", "coverage", "economics", "admission", "execution", "qualification"} {
		m.corporateActionSources = clone(original)
		r := &m.corporateActionSources[len(original)-1]
		switch grant {
		case "provider":
			r.ProviderAttested = true
		case "coverage":
			r.CoverageComplete = true
		case "economics":
			r.EconomicApplicationReady = true
		case "admission":
			r.AdmissionAvailable = true
		case "execution":
			r.ExecutionAvailable = true
		case "qualification":
			r.QualificationCredit = true
		}
		r.SHA256 = ""
		r.SHA256 = workerplan.Hash(*r)
		if _, err := m.CorporateActionSources(ctx); !errors.Is(err, ErrCorporateActionSourceInvalid) {
			t.Fatal("rehashed authority accepted", grant, err)
		}
	}
}
func corporateSourceMigration(t *testing.T, p *Postgres) {
	t.Helper()
	ctx := context.Background()
	var before, after, marker, hash string
	q := "SELECT jsonb_agg(payload ORDER BY sequence)::text FROM mantik_corporate_action_sources"
	if err := p.pool.QueryRow(ctx, q).Scan(&before); err != nil {
		t.Fatal(err)
	}
	if _, err := p.pool.Exec(ctx, `DELETE FROM mantik_schema WHERE version=71; COMMENT ON TABLE mantik_corporate_action_sources IS 'rollback marker'; CREATE FUNCTION reject_corporate_migration() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.version=71 THEN RAISE EXCEPTION 'manufactured migration rejection'; END IF; RETURN NEW; END $$; CREATE TRIGGER reject_corporate_migration BEFORE INSERT ON mantik_schema FOR EACH ROW EXECUTE FUNCTION reject_corporate_migration()`); err != nil {
		t.Fatal(err)
	}
	if err := p.migrate(ctx); err == nil {
		t.Fatal("migration rejection ignored")
	}
	if err := p.pool.QueryRow(ctx, "SELECT obj_description('mantik_corporate_action_sources'::regclass)").Scan(&marker); err != nil || marker != "rollback marker" {
		t.Fatal("migration escaped rollback", marker, err)
	}
	if _, err := p.pool.Exec(ctx, "DROP TRIGGER reject_corporate_migration ON mantik_schema; DROP FUNCTION reject_corporate_migration()"); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := p.migrate(ctx); err != nil {
			t.Fatal(err)
		}
	}
	if err := p.pool.QueryRow(ctx, q).Scan(&after); err != nil || before != after {
		t.Fatal("migration changed retained source", err)
	}
	if err := p.pool.QueryRow(ctx, "SELECT sha256 FROM mantik_schema WHERE version=71").Scan(&hash); err != nil {
		t.Fatal(err)
	}
	if _, err := p.pool.Exec(ctx, "UPDATE mantik_schema SET sha256='damaged' WHERE version=71"); err != nil {
		t.Fatal(err)
	}
	if err := p.migrate(ctx); err == nil {
		t.Fatal("checksum corruption accepted")
	}
	if _, err := p.pool.Exec(ctx, "UPDATE mantik_schema SET sha256=$1 WHERE version=71", hash); err != nil {
		t.Fatal(err)
	}
	// Rehashed false economic authority must fail actual SQL readback too.
	cat, err := p.CorporateActionSources(ctx)
	if err != nil {
		t.Fatal(err)
	}
	record := cat.Records[1]
	original, _ := json.Marshal(record)
	record.EconomicApplicationReady = true
	record.SHA256 = ""
	record.SHA256 = workerplan.Hash(record)
	raw, _ := json.Marshal(record)
	if _, err = p.pool.Exec(ctx, "UPDATE mantik_corporate_action_sources SET payload=$1 WHERE id=$2", raw, record.Command.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = p.CorporateActionSources(ctx); !errors.Is(err, ErrCorporateActionSourceInvalid) {
		t.Fatal("SQL rehashed economic grant accepted", err)
	}
	if _, err = p.pool.Exec(ctx, "UPDATE mantik_corporate_action_sources SET payload=$1 WHERE id=$2", original, record.Command.ID); err != nil {
		t.Fatal(err)
	}
}

func assertRestoredCorporateSource(t *testing.T, p *Postgres, owner string) {
	t.Helper()
	ctx := context.Background()
	cat, err := p.CorporateActionSources(ctx)
	if err != nil || cat.ReceiverID != owner || len(cat.Records) != 1 || cat.Records[0].Command.ID != "recovery-corporate-source" {
		t.Fatal("restored original corporate source missing", err)
	}
	r := cat.Records[0]
	assertCorporateSourceFlags(t, r)
	want := corporateSourceFixture(owner)
	want.ID = r.Command.ID
	if !reflect.DeepEqual(r.Command, want) {
		t.Fatal("restored original page changed")
	}
	retry, err := p.RetainCorporateActionSource(ctx, r.Command, func() time.Time { t.Fatal("restored source retry read clock"); return time.Time{} })
	if err != nil || !retry.Duplicate || !reflect.DeepEqual(retry.Record, r) {
		t.Fatal("restored source retry changed receipt", err)
	}
	// Restore the exact original payload before the outer snapshot identity check.
	original, _ := json.Marshal(r)
	r.CoverageComplete = true
	r.SHA256 = ""
	r.SHA256 = workerplan.Hash(r)
	raw, _ := json.Marshal(r)
	if _, err := p.pool.Exec(ctx, "UPDATE mantik_corporate_action_sources SET payload=$1 WHERE id=$2", raw, r.Command.ID); err != nil {
		t.Fatal(err)
	}
	_, readErr := p.CorporateActionSources(ctx)
	_, restoreErr := p.pool.Exec(ctx, "UPDATE mantik_corporate_action_sources SET payload=$1 WHERE id=$2", original, r.Command.ID)
	if restoreErr != nil || !errors.Is(readErr, ErrCorporateActionSourceInvalid) {
		t.Fatal("restored self-rehashed coverage grant accepted or original restore failed", readErr, restoreErr)
	}
	t.Logf("restored corporate source=%s; original bytes, clock-free retry and rehashed coverage rejection passed", cat.Records[0].SHA256)
}
