package storage

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"sync"
	"testing"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/extraction"
	"git.subcult.tv/subculture-collective/mantik/internal/news"
)

type extractionBackend interface {
	ExtractionStore
	ResearchReceiverStore
	Store
}

func TestMemoryExtractionLedger(t *testing.T) { extractionContract(t, NewMemory()) }
func TestPostgresContractsExtractionLedger(t *testing.T) {
	extractionContract(t, timerPostgres(t, "extractionledger"))
}
func extractionContract(t *testing.T, s extractionBackend) {
	t.Helper()
	ctx := context.Background()
	receiver, err := s.ResearchSnapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	a := news.FixtureArticle()
	at := a.ReceivedAt.Add(time.Hour)
	now := func() time.Time { return at }
	noClock := func() time.Time { t.Error("exact retry sampled clock"); return time.Time{} }
	start := func(id string) extraction.Attempt {
		t.Helper()
		r, e := s.StartExtraction(ctx, receiver.ID, id, a, "event", now)
		if e != nil {
			t.Fatal(e)
		}
		return r
	}
	pending := start("one")
	if pending.Validate() != nil {
		t.Fatal("invalid pending attempt")
	}
	if retry, e := s.StartExtraction(ctx, receiver.ID, "one", a, "event", noClock); e != nil || !reflect.DeepEqual(retry, pending) {
		t.Fatal("pending retry changed", e)
	}
	changed := a
	changed.Original += "different"
	changed.OriginalSHA256 = news.SHA256(changed.Original)
	if _, e := s.StartExtraction(ctx, receiver.ID, "one", changed, "event", now); !errors.Is(e, extraction.ErrConflict) {
		t.Fatal("source rebinding accepted", e)
	}
	if _, e := s.StartExtraction(ctx, "receiver-abcdefghijklmnop", "another", a, "event", now); !errors.Is(e, ErrReceiverMismatch) {
		t.Fatal("wrong receiver accepted", e)
	}
	raw, _ := json.Marshal(extraction.FixtureResponse())
	var wg sync.WaitGroup
	all := make(chan extraction.Attempt, 8)
	fail := make(chan error, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r, e := s.CompleteExtraction(ctx, receiver.ID, "one", raw, func() time.Time { return at.Add(time.Second) })
			if e != nil {
				fail <- e
			} else {
				all <- r
			}
		}()
	}
	wg.Wait()
	close(all)
	close(fail)
	for e := range fail {
		t.Fatal(e)
	}
	var done extraction.Attempt
	for r := range all {
		if done.ID != "" && !reflect.DeepEqual(done, r) {
			t.Fatal("concurrent completion identities diverged")
		}
		done = r
	}
	if done.Status != "completed" || done.Result.Outcome != "extracted" || done.Validate() != nil {
		t.Fatal(done)
	}
	if retry, e := s.CompleteExtraction(ctx, receiver.ID, "one", raw, noClock); e != nil || !reflect.DeepEqual(retry, done) {
		t.Fatal("completed retry changed", e)
	}
	if _, e := s.CompleteExtraction(ctx, receiver.ID, "one", append(append([]byte{}, raw...), ' '), now); !errors.Is(e, extraction.ErrConflict) {
		t.Fatal("different raw bytes accepted", e)
	}
	start("abstain")
	bad := []byte{0xff, 0xfe}
	if r, e := s.CompleteExtraction(ctx, receiver.ID, "abstain", bad, now); e != nil || r.Result.Outcome != "abstain" || !r.Result.Artifact.CaptureComplete {
		t.Fatal(r, e)
	}
	start("expired")
	if r, e := s.CompleteExtraction(ctx, receiver.ID, "expired", raw, func() time.Time { return at.Add(30 * time.Second) }); e != nil || r.Status != "expired" || r.Result.Outcome != "extracted" {
		t.Fatal(r, e)
	}
	start("pending")
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if _, e := s.CompleteExtraction(cancelled, receiver.ID, "pending", raw, now); e == nil {
		t.Fatal("cancelled completion committed")
	}
	if _, e := s.CompleteExtraction(ctx, receiver.ID, "pending", raw, func() time.Time { return at.Add(-time.Second) }); !errors.Is(e, extraction.ErrConflict) {
		t.Fatal("backward clock committed", e)
	}
	h, err := s.Extractions(ctx)
	if err != nil || h.Validate() != nil || h.AttemptCount != 4 || h.StatusCounts["pending"] != 1 || h.StatusCounts["expired"] != 1 || h.StatusCounts["completed"] != 2 {
		t.Fatal(h, err)
	}
	h.Attempts[0].ID = "mutated"
	done.Article.Original = "mutated"
	if original, e := s.Extraction(ctx, "one"); e != nil || original.Article.Original != a.Original {
		t.Fatal("caller alias changed attempt", e)
	}
	if pg, ok := s.(*Postgres); ok {
		if _, e := pg.pool.Exec(ctx, "ALTER TABLE mantik_news_extractions ADD CONSTRAINT deny_terminal_rewrite CHECK(id!='one') NOT VALID"); e != nil {
			t.Fatal(e)
		}
		if _, e := s.CompleteExtraction(ctx, receiver.ID, "one", raw, noClock); e != nil {
			t.Fatal("exact terminal retry attempted to rewrite the receipt", e)
		}
		if _, e := pg.pool.Exec(ctx, "ALTER TABLE mantik_news_extractions DROP CONSTRAINT deny_terminal_rewrite"); e != nil {
			t.Fatal(e)
		}
		config := pg.pool.Config().ConnString()
		pg.Close()
		reopened, e := OpenPostgres(ctx, config)
		if e != nil {
			t.Fatal(e)
		}
		defer reopened.Close()
		s = reopened
		if after, e := s.Extractions(ctx); e != nil || after.AttemptCount != 4 {
			t.Fatal("restart lost attempts", e)
		}
		if _, e = reopened.pool.Exec(ctx, "ALTER TABLE mantik_news_extractions ADD CONSTRAINT reject_extraction_finish CHECK(id!='pending' OR payload->>'status'='pending')"); e != nil {
			t.Fatal(e)
		}
		if _, e = s.CompleteExtraction(ctx, receiver.ID, "pending", raw, now); e == nil {
			t.Fatal("failed write acknowledged")
		}
		if r, e := s.Extraction(ctx, "pending"); e != nil || r.Status != "pending" || r.Result != nil {
			t.Fatal("failed write changed source pin", e)
		}
		if _, e = reopened.pool.Exec(ctx, "ALTER TABLE mantik_news_extractions DROP CONSTRAINT reject_extraction_finish"); e != nil {
			t.Fatal(e)
		}
	}
	for i := 4; i < extraction.MaxAttempts; i++ {
		startResult, e := s.StartExtraction(ctx, receiver.ID, fmt.Sprintf("capacity-%d", i), a, "event", now)
		if e != nil || startResult.Status != "pending" {
			t.Fatal(i, e)
		}
	}
	if _, e := s.StartExtraction(ctx, receiver.ID, "overflow", a, "event", now); !errors.Is(e, extraction.ErrCapacity) {
		t.Fatal("capacity did not preserve failures", e)
	}
	if _, e := s.StartExtraction(ctx, receiver.ID, "one", a, "event", noClock); e != nil {
		t.Fatal("retry blocked at capacity", e)
	}
	if full, e := s.Extractions(ctx); e != nil || full.AttemptCount != 32 {
		t.Fatal("history truncated", e)
	}
	if pg, ok := s.(*Postgres); ok {
		if _, e := pg.pool.Exec(ctx, `UPDATE mantik_news_extractions SET payload=jsonb_set(payload,'{result,qualification_credit}','true') WHERE id='one'`); e != nil {
			t.Fatal(e)
		}
	} else {
		m := s.(*Memory)
		r := m.extractions["one"]
		r.Result.QualificationCredit = true
		m.extractions["one"] = r
	}
	if _, e := s.Extractions(ctx); e == nil {
		t.Fatal("corrupt history returned partial success")
	}
}

func TestPostgresContractsExtractionUpgrade(t *testing.T) {
	s := timerPostgres(t, "extractionupgrade")
	ctx := context.Background()
	catalog, _ := s.NewsCases(ctx)
	r := news.Fixture()
	if _, e := s.AdmitNews(ctx, catalog.ReceiverID, r, time.Now); e != nil {
		t.Fatal(e)
	}
	var before string
	if e := s.pool.QueryRow(ctx, "SELECT payload::text FROM mantik_news_cases WHERE singleton").Scan(&before); e != nil {
		t.Fatal(e)
	}
	if _, e := s.pool.Exec(ctx, "DROP TABLE mantik_news_extraction_admissions; DROP TABLE mantik_news_extractions; DELETE FROM mantik_schema WHERE version>=21"); e != nil {
		t.Fatal(e)
	}
	config := s.pool.Config().ConnString()
	s.Close()
	upgraded, e := OpenPostgres(ctx, config)
	if e != nil {
		t.Fatal(e)
	}
	defer upgraded.Close()
	var after string
	if e = upgraded.pool.QueryRow(ctx, "SELECT payload::text FROM mantik_news_cases WHERE singleton").Scan(&after); e != nil || after != before {
		t.Fatal("20->21 changed older news state", e)
	}
	if _, e = upgraded.pool.Exec(ctx, "UPDATE mantik_schema SET sha256='tampered' WHERE version=21"); e != nil {
		t.Fatal(e)
	}
	if bad, e := OpenPostgres(ctx, config); e == nil {
		bad.Close()
		t.Fatal("changed migration checksum accepted")
	}
}

func TestMemoryExtractionCapacityRace(t *testing.T) { extractionCapacityRace(t, NewMemory()) }
func TestPostgresContractsExtractionCapacityRace(t *testing.T) {
	extractionCapacityRace(t, timerPostgres(t, "extractioncapacity"))
}
func extractionCapacityRace(t *testing.T, s extractionBackend) {
	t.Helper()
	ctx := context.Background()
	receiver, _ := s.ResearchSnapshot(ctx)
	article := news.FixtureArticle()
	now := func() time.Time { return article.ReceivedAt.Add(time.Hour) }
	for i := 0; i < 31; i++ {
		if _, e := s.StartExtraction(ctx, receiver.ID, fmt.Sprintf("before-%d", i), article, "event", now); e != nil {
			t.Fatal(e)
		}
	}
	results := make(chan error, 8)
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, e := s.StartExtraction(ctx, receiver.ID, fmt.Sprintf("race-%d", i), article, "event", now)
			results <- e
		}(i)
	}
	wg.Wait()
	close(results)
	success, rejected := 0, 0
	for e := range results {
		if e == nil {
			success++
		} else if errors.Is(e, extraction.ErrCapacity) {
			rejected++
		} else {
			t.Fatal(e)
		}
	}
	h, e := s.Extractions(ctx)
	if e != nil || success != 1 || rejected != 7 || h.AttemptCount != 32 || h.StatusCounts["pending"] != 32 {
		t.Fatal(success, rejected, h, e)
	}
}
