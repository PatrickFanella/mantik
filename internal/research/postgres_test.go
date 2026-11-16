package research

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"strconv"
	"testing"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/storage"
	"git.subcult.tv/subculture-collective/mantik/migrations"
)

func TestPostgresContractsResearchDesk(t *testing.T) {
	dsn := os.Getenv("MANTIK_DESK_TEST_DATABASE_URL")
	if dsn == "" {
		dsn = os.Getenv("MANTIK_TEST_DATABASE_URL")
	}
	if dsn == "" {
		t.Skip("new disposable database required")
	}
	ctx := context.Background()
	s, e := storage.OpenPostgres(ctx, dsn)
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	j, e := OpenPostgresJournal(ctx, dsn)
	if e != nil {
		t.Fatal(e)
	}
	defer j.Close()
	var digest string
	if e = j.pool.QueryRow(ctx, "SELECT sha256 FROM mantik_schema WHERE version=123").Scan(&digest); e != nil || digest != fmt.Sprintf("%x", sha256.Sum256([]byte(migrations.ResearchDeskEvents))) {
		t.Fatal("desk migration missing from bootstrap registry", digest, e)
	}
	d := fixture()
	d.ID = "sql-desk-" + strconv.FormatInt(time.Now().UnixNano(), 36)
	d.MaxCalls = 3
	caller := &scripted{t: t, replies: []string{`{"tool":"compose_spec","spec":{"score":{"op":"unreviewed"}}}`, `{"tool":"run_backtest","spec":` + validSpec + `}`, `{"tool":"done"}`}}
	captured := &captureJournal{Journal: j}
	result, e := Run(ctx, captured, s, s, caller, d)
	if e != nil || len(result.TrialIDs) != 2 {
		t.Fatal(result, e)
	}
	if _, e = j.pool.Exec(ctx, migrations.ResearchDeskEvents); e != nil {
		t.Fatal("replay migration", e)
	}
	reopened, e := OpenPostgresJournal(ctx, dsn)
	if e != nil {
		t.Fatal(e)
	}
	defer reopened.Close()
	events, e := reopened.Events(ctx, d.ID)
	if e != nil || len(events) == 0 {
		t.Fatal(events, e)
	}
	for i, event := range events {
		if string(marshal(event)) != string(marshal(captured.written[i])) {
			t.Errorf("event %d roundtrip changed bytes", i)
		}
	}
	retry, e := Run(ctx, reopened, s, s, caller, d)
	if e != nil || retry.EventsSHA256 != result.EventsSHA256 || caller.calls != 3 {
		t.Fatal("retry changed history", retry, e)
	}
	failed := d
	failed.ID += "-failure"
	unavailable := &scripted{t: t, fail: errors.New("private fixture diagnostic")}
	firstFailure, e := Run(ctx, j, s, s, unavailable, failed)
	if e == nil || firstFailure.Status != "model_unavailable" {
		t.Fatal(firstFailure, e)
	}
	failureRetry, e := Run(ctx, reopened, s, s, unavailable, failed)
	if e != nil || firstFailure.EventsSHA256 != failureRetry.EventsSHA256 || unavailable.calls != 1 {
		t.Fatal("failed-call hash changed after SQL roundtrip", firstFailure, failureRetry, e)
	}
	for _, sql := range []string{"UPDATE mantik_research_desk_events SET kind=kind", "DELETE FROM mantik_research_desk_events", "TRUNCATE mantik_research_desk_events"} {
		if _, e = j.pool.Exec(ctx, sql); e == nil {
			t.Fatal("mutated evidence", sql)
		}
	}
	if e = j.Append(ctx, d.ID, 0, Event{Kind: "start", At: events[0].At, Body: events[0].Body}); e != nil {
		t.Fatal("exact retry", e)
	}
	if e = j.Append(ctx, d.ID, 0, Event{Kind: "start", At: time.Now(), Body: events[0].Body}); !errors.Is(e, ErrConflict) {
		t.Fatal("changed retry", e)
	}
	unlock, e := j.Lock(ctx, "fence")
	if e != nil {
		t.Fatal(e)
	}
	if release, e := reopened.Lock(ctx, "fence"); e == nil {
		release()
		t.Fatal("two executors admitted")
	}
	unlock()
	for _, id := range result.TrialIDs {
		r, e := s.StrategyTrial(ctx, id)
		if e != nil || r.Outcome == nil {
			t.Fatal(r, e)
		}
	}
}
