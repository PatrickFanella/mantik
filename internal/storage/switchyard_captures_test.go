package storage

import (
	"context"
	"errors"
	"fmt"
	"git.subcult.tv/subculture-collective/mantik/internal/extraction"
	"git.subcult.tv/subculture-collective/mantik/internal/news"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sync"
	"testing"
	"time"
)

type switchyardCaptureBackend interface {
	SwitchyardCaptureStore
	ResearchReceiverStore
	ExtractionStore
	Store
}

func switchyardCaptureFixture(t *testing.T) (extraction.SwitchyardReceipt, extraction.SwitchyardReceipt) {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(503)
		_, _ = w.Write([]byte("private failure"))
	}))
	defer server.Close()
	var receipts []extraction.SwitchyardReceipt
	_, err := extraction.RunSwitchyard(context.Background(), news.FixtureArticle(), "event", extraction.SwitchyardConfig{Endpoint: server.URL, Model: extraction.SwitchyardModel, MaxTokens: 1024, ReasoningEffort: "low"}, "", func(r extraction.SwitchyardReceipt) error { receipts = append(receipts, r); return nil })
	if err != nil || len(receipts) != 2 {
		t.Fatal(err)
	}
	return receipts[0], receipts[1]
}
func TestMemorySwitchyardCaptures(t *testing.T) { switchyardCaptureContract(t, NewMemory()) }
func TestPostgresContractsSwitchyardCaptures(t *testing.T) {
	switchyardCaptureContract(t, timerPostgres(t, "localcaptures"))
}
func switchyardCaptureContract(t *testing.T, s switchyardCaptureBackend) {
	t.Helper()
	ctx := context.Background()
	receiver, e := s.ResearchSnapshot(ctx)
	if e != nil {
		t.Fatal(e)
	}
	before, e := s.Snapshot(ctx)
	if e != nil {
		t.Fatal(e)
	}
	p, f := switchyardCaptureFixture(t)
	at := time.Now().UTC()
	now := func() time.Time { return at }
	noClock := func() time.Time { t.Error("retry read clock"); return time.Time{} }
	a, e := s.ImportSwitchyardCapture(ctx, receiver.ID, "one", p, nil, now)
	if e != nil {
		t.Fatal(e)
	}
	b, e := s.ImportSwitchyardCapture(ctx, receiver.ID, "two", p, &f, now)
	if e != nil {
		t.Fatal(e)
	}
	if a.Inspection.State != "prepared_only" || b.Inspection.State != "finished" || b.Finished.Result.Outcome != "abstain" {
		t.Fatal(a, b)
	}
	if _, e = s.ImportSwitchyardCapture(ctx, receiver.ID, "one", p, &f, now); !errors.Is(e, extraction.ErrConflict) {
		t.Fatal("unknown import overwritten", e)
	}
	if _, e = s.ImportSwitchyardCapture(ctx, "receiver-abcdefghijklmnop", "wrong", p, nil, now); !errors.Is(e, ErrReceiverMismatch) {
		t.Fatal(e)
	}
	if _, e = s.ImportSwitchyardCapture(ctx, receiver.ID, "badclock", p, nil, func() time.Time { return time.Time{} }); e == nil {
		t.Fatal("invalid import time accepted")
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if _, e = s.ImportSwitchyardCapture(cancelled, receiver.ID, "cancel", p, nil, now); e == nil {
		t.Fatal("cancelled import accepted")
	}
	var wg sync.WaitGroup
	results := make(chan error, 8)
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r, e := s.ImportSwitchyardCapture(ctx, receiver.ID, "two", p, &f, noClock)
			if e == nil && !reflect.DeepEqual(r, b) {
				e = fmt.Errorf("retry diverged")
			}
			results <- e
		}()
	}
	wg.Wait()
	close(results)
	for e := range results {
		if e != nil {
			t.Fatal(e)
		}
	}
	h, e := s.SwitchyardCaptures(ctx)
	if e != nil || h.Validate() != nil || h.CaptureCount != 2 {
		t.Fatal(h, e)
	}
	got, e := s.SwitchyardCapture(ctx, "two")
	if e != nil || !reflect.DeepEqual(got, b) {
		t.Fatal(e)
	}
	got.Finished.Response.Base64 = "mutated"
	if r, e := s.SwitchyardCapture(ctx, "two"); e != nil || !reflect.DeepEqual(r, b) {
		t.Fatal("alias corrupted stored capture", e)
	}
	recorded, e := s.Extractions(ctx)
	if e != nil || recorded.AttemptCount != 0 {
		t.Fatal("import appeared in recorded ledger", e)
	}
	after, e := s.Snapshot(ctx)
	if e != nil || !reflect.DeepEqual(before, after) {
		t.Fatal("import changed operations", e)
	}
	research, e := s.ResearchSnapshot(ctx)
	if e != nil || !reflect.DeepEqual(receiver, research) {
		t.Fatal("import changed research", e)
	}
	if pg, ok := s.(*Postgres); ok {
		if _, e = pg.pool.Exec(ctx, "ALTER TABLE mantik_switchyard_extraction_captures ADD CONSTRAINT reject_rewrite CHECK(id!='two') NOT VALID"); e != nil {
			t.Fatal(e)
		}
		if _, e = s.ImportSwitchyardCapture(ctx, receiver.ID, "two", p, &f, noClock); e != nil {
			t.Fatal("retry rewrote row", e)
		}
		if _, e = pg.pool.Exec(ctx, "ALTER TABLE mantik_switchyard_extraction_captures DROP CONSTRAINT reject_rewrite; ALTER TABLE mantik_switchyard_extraction_captures ADD CONSTRAINT reject_insert CHECK(id!='failed')"); e != nil {
			t.Fatal(e)
		}
		if _, e = s.ImportSwitchyardCapture(ctx, receiver.ID, "failed", p, &f, now); e == nil {
			t.Fatal("failed persistence acknowledged")
		}
		if _, e = s.SwitchyardCapture(ctx, "failed"); !errors.Is(e, extraction.ErrNotFound) {
			t.Fatal("failed persistence retained receipt", e)
		}
		config := pg.pool.Config().ConnString()
		pg.Close()
		reopened, e := OpenPostgres(ctx, config)
		if e != nil {
			t.Fatal(e)
		}
		defer reopened.Close()
		s = reopened
		if r, e := s.SwitchyardCapture(ctx, "two"); e != nil || !reflect.DeepEqual(r, b) {
			t.Fatal("restart changed evidence", e)
		}
	}
	for i := 2; i < extraction.MaxAttempts-1; i++ {
		if _, e = s.ImportSwitchyardCapture(ctx, receiver.ID, fmt.Sprintf("cap-%d", i), p, nil, now); e != nil {
			t.Fatal(e)
		}
	}
	results = make(chan error, 8)
	for i := range 8 {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, e := s.ImportSwitchyardCapture(ctx, receiver.ID, fmt.Sprintf("race-%d", i), p, nil, now)
			results <- e
		}(i)
	}
	wg.Wait()
	close(results)
	passed, full := 0, 0
	for e := range results {
		if e == nil {
			passed++
		} else if errors.Is(e, extraction.ErrCapacity) {
			full++
		} else {
			t.Fatal(e)
		}
	}
	if passed != 1 || full != 7 {
		t.Fatal("capacity race", passed, full)
	}
	if _, e = s.ImportSwitchyardCapture(ctx, receiver.ID, "two", p, &f, noClock); e != nil {
		t.Fatal("full retry failed", e)
	}
	if pg, ok := s.(*Postgres); ok {
		if _, e = pg.pool.Exec(ctx, `UPDATE mantik_switchyard_extraction_captures SET payload=jsonb_set(payload,'{inspection,outcome}','"extracted"') WHERE id='one'`); e != nil {
			t.Fatal(e)
		}
	} else {
		m := s.(*Memory)
		r := m.switchyardCaptures["one"]
		r.Inspection.Outcome = "extracted"
		m.switchyardCaptures["one"] = r
	}
	if _, e = s.SwitchyardCaptures(ctx); e == nil {
		t.Fatal("corrupt ledger returned partial history")
	}
}
func TestPostgresContractsSwitchyardCaptureUpgrade(t *testing.T) {
	ctx := context.Background()
	s := timerPostgres(t, "localcaptureupgrade")
	receiver, e := s.ResearchSnapshot(ctx)
	if e != nil {
		t.Fatal(e)
	}
	a, e := s.StartExtraction(ctx, receiver.ID, "older", news.FixtureArticle(), "event", time.Now)
	if e != nil {
		t.Fatal(e)
	}
	before, e := s.Snapshot(ctx)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = s.pool.Exec(ctx, "DROP TABLE mantik_candidate_worker_intents; DROP TABLE mantik_candidate_datasets; DROP TABLE mantik_candidate_registrations; DROP TABLE mantik_fixture_capital; DROP TABLE mantik_paper_ledger_rehearsals; DROP TABLE mantik_paper_accounts; DROP TABLE mantik_switchyard_capture_admissions; DROP TABLE mantik_switchyard_extraction_captures; DELETE FROM mantik_schema WHERE version>=25"); e != nil {
		t.Fatal(e)
	}
	config := s.pool.Config().ConnString()
	s.Close()
	up, e := OpenPostgres(ctx, config)
	if e != nil {
		t.Fatal(e)
	}
	defer up.Close()
	old, e := up.Extraction(ctx, a.ID)
	if e != nil || !reflect.DeepEqual(old, a) {
		t.Fatal("upgrade changed fixture attempt", e)
	}
	after, e := up.Snapshot(ctx)
	if e != nil || !reflect.DeepEqual(before, after) {
		t.Fatal("upgrade changed operations", e)
	}
	p, f := switchyardCaptureFixture(t)
	r, e := up.ImportSwitchyardCapture(ctx, receiver.ID, "capture", p, &f, time.Now)
	if e != nil {
		t.Fatal(e)
	}
	// Verify captures can be retained after the upgrade.
	if r.Validate() != nil {
		t.Fatal("invalid upgraded capture")
	}
	if _, e = up.pool.Exec(ctx, "UPDATE mantik_schema SET sha256='changed' WHERE version=25"); e != nil {
		t.Fatal(e)
	}
	if bad, e := OpenPostgres(ctx, config); e == nil {
		bad.Close()
		t.Fatal("changed migration accepted")
	}
}
