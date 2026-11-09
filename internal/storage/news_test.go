package storage

import (
	"context"
	"errors"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/news"
	"git.subcult.tv/subculture-collective/mantik/internal/newscase"
	"git.subcult.tv/subculture-collective/mantik/internal/workflow"
)

type newsBackend interface {
	Store
	NewsStore
	ResearchReceiverStore
}

func TestMemoryNewsAdmission(t *testing.T) { newsContracts(t, NewMemory()) }
func TestPostgresContractsNewsAdmission(t *testing.T) {
	newsContracts(t, timerPostgres(t, "newscases"))
}
func newsContracts(t *testing.T, s newsBackend) {
	ctx := context.Background()
	before, err := s.Snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := s.NewsCases(ctx)
	if err != nil {
		t.Fatal(err)
	}
	receiver := catalog.ReceiverID
	r := news.Fixture()
	at := r.ReceivedAt.Add(time.Hour)
	now := func() time.Time { return at }
	if _, err = s.AdmitNews(ctx, "receiver-abcdefghijklmnop", r, now); !errors.Is(err, ErrReceiverMismatch) {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	var fresh atomic.Int32
	for range 8 {
		wg.Go(func() {
			receipt, err := s.AdmitNews(ctx, receiver, r, now)
			if err != nil {
				t.Error(err)
			} else if !receipt.Duplicate {
				fresh.Add(1)
			}
		})
	}
	wg.Wait()
	if fresh.Load() != 1 {
		t.Fatal("concurrent admission repeated effects")
	}
	receipt, err := s.AdmitNews(ctx, receiver, r, now)
	if err != nil || !receipt.Duplicate {
		t.Fatal(err)
	}
	record, err := s.NewsRecord(ctx, receipt.RecordKey)
	if err != nil || record.Recording.Original != r.Original || record.Result.RecordingSHA256 != receipt.RecordingSHA256 {
		t.Fatal("source/receipt missing", err)
	}
	catalog, err = s.NewsCases(ctx)
	if err != nil || len(catalog.Cases) != 1 || catalog.Cases[0].Version != 1 {
		t.Fatal(catalog, err)
	}
	c := catalog.Cases[0]
	if _, err = s.CloseNewsCase(ctx, receiver, c.ID, 1, now); err != nil {
		t.Fatal(err)
	}
	// Repeated coverage links the same event, reopening for review without any
	// assertion of independent corroboration. Source records remain separate.
	distributed := news.Fixture()
	distributed.SourceID = "distribution"
	if _, err = s.AdmitNews(ctx, receiver, distributed, now); err != nil {
		t.Fatal(err)
	}
	if _, err = s.CloseNewsCase(ctx, receiver, c.ID, 1, now); !errors.Is(err, newscase.ErrConflict) {
		t.Fatal("stale closure erased new evidence", err)
	}
	retained, err := s.NewsCases(ctx)
	if err != nil || len(retained.Cases) != 1 || retained.Cases[0].Generation != 2 || len(retained.Cases[0].RecordKeys) != 2 {
		t.Fatal(retained, err)
	}
	// Race a close based on the displayed version against another article. The
	// new evidence must remain open regardless of lock acquisition order.
	competing := news.Fixture()
	competing.SourceID = "competing"
	wg.Go(func() {
		_, err := s.CloseNewsCase(ctx, receiver, c.ID, 2, now)
		if err != nil && !errors.Is(err, newscase.ErrConflict) {
			t.Error(err)
		}
	})
	wg.Go(func() {
		if _, err := s.AdmitNews(ctx, receiver, competing, now); err != nil {
			t.Error(err)
		}
	})
	wg.Wait()
	retained, err = s.NewsCases(ctx)
	if err != nil || retained.Cases[0].Status != "open" || retained.Cases[0].Version != 3 {
		t.Fatal("racing close hid admitted evidence", err)
	}
	if pg, ok := s.(*Postgres); ok {
		config := pg.pool.Config().ConnString()
		pg.Close()
		reopened, err := OpenPostgres(ctx, config)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(reopened.Close)
		s = reopened
		again, err := s.NewsCases(ctx)
		if err != nil || !reflect.DeepEqual(again, retained) {
			t.Fatal("restart lost case links", err)
		}
		stored, err := s.NewsRecord(ctx, receipt.RecordKey)
		if err != nil || !reflect.DeepEqual(stored, record) {
			t.Fatal("restart lost original or receipt", err)
		}
		if _, err = reopened.pool.Exec(ctx, "ALTER TABLE mantik_news_cases ADD CONSTRAINT reject_news_write CHECK(NOT (payload->'records' ? '"+newscase.RecordKey(news.Recording{SourceID: "forced", ArticleID: r.ArticleID, EvidenceClass: r.EvidenceClass, Revision: 1})+"'))"); err != nil {
			t.Fatal(err)
		}
		rejected := news.Fixture()
		rejected.SourceID = "forced"
		if _, err = s.AdmitNews(ctx, receiver, rejected, now); err == nil {
			t.Fatal("failed persistence acknowledged")
		}
		again, err = s.NewsCases(ctx)
		if err != nil || !reflect.DeepEqual(again, retained) {
			t.Fatal("partial case/evidence write", err)
		}
		if _, err = s.NewsRecord(ctx, newscase.RecordKey(rejected)); !errors.Is(err, newscase.ErrNotFound) {
			t.Fatal("failed original retained", err)
		}
		if _, err = reopened.pool.Exec(ctx, "ALTER TABLE mantik_news_cases DROP CONSTRAINT reject_news_write"); err != nil {
			t.Fatal(err)
		}
	}
	after, err := s.Snapshot(ctx)
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatal("news modified fixture/routing/operations", err)
	}
	retained.Cases[0].RecordKeys[0] = "tampered"
	record.Recording.Claims[0].Assertion = "tampered"
	actual, err := s.NewsCases(ctx)
	if err != nil || actual.Cases[0].RecordKeys[0] == "tampered" {
		t.Fatal("alias altered retained case")
	}
	stored, err := s.NewsRecord(ctx, receipt.RecordKey)
	if err != nil || stored.Recording.Claims[0].Assertion == "tampered" {
		t.Fatal("alias altered retained source")
	}
	if _, err = s.Ingest(ctx, workflow.Fixtures()[1]); err != nil {
		t.Fatal(err)
	}
	unchanged, err := s.NewsCases(ctx)
	if err != nil || !reflect.DeepEqual(actual, unchanged) {
		t.Fatal("fixture write discarded news", err)
	}
}

func TestPostgresContractsNewsUpgrade(t *testing.T) {
	s := timerPostgres(t, "newsupgrade")
	ctx := context.Background()
	before, err := s.Snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	receiver, err := s.ResearchSnapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	config := s.pool.Config().ConnString()
	if _, err = s.pool.Exec(ctx, "DROP TABLE mantik_paper_evaluation_declarations; DROP TABLE mantik_decision_reviews; DROP TABLE mantik_social_watch_journal; DROP TABLE mantik_paper_outcomes; DROP TABLE mantik_owned_allocations; DROP TABLE mantik_paper_decisions; DROP TABLE mantik_paper_strategies; DROP TABLE mantik_allocation_policies; ALTER TABLE mantik_candidate_registrations DROP COLUMN generation_link_sha256 CASCADE; DROP TABLE mantik_generated_candidate_links; DROP TABLE mantik_candidate_generation_outcomes; DROP TABLE mantik_candidate_generation_datasets; DROP TABLE mantik_candidate_generation_intents; DROP TABLE mantik_candidate_worker_outcomes; DROP TABLE mantik_candidate_worker_intents; DROP TABLE mantik_candidate_datasets; DROP TABLE mantik_candidate_registrations; DROP TABLE mantik_fixture_capital; DROP TABLE mantik_paper_ledger_rehearsals; DROP TABLE mantik_paper_accounts; DROP TABLE mantik_switchyard_capture_admissions; DROP TABLE mantik_switchyard_extraction_captures; DROP TABLE mantik_local_capture_admissions; DROP TABLE mantik_local_extraction_captures; DROP TABLE mantik_news_extraction_admissions; DROP TABLE mantik_news_extractions; DROP TABLE mantik_external_trial_attempt_outputs; DROP TABLE mantik_external_trial_attempts; DROP TABLE mantik_external_trial_job_inputs; DROP TABLE mantik_external_trial_jobs; DROP TABLE mantik_trial_run_artifacts; DROP TABLE mantik_trial_runs; DROP TABLE mantik_trial_attempts; DROP TABLE mantik_hypothesis_trials; DROP TABLE mantik_hypothesis_publications; DROP TABLE mantik_news_reviews; DROP TABLE mantik_news_cases; DROP TABLE IF EXISTS mantik_paper_evaluation_assessments; DROP TABLE IF EXISTS mantik_owned_admission_reviews; DROP TABLE IF EXISTS mantik_owned_allocation_archive; DELETE FROM mantik_schema WHERE version>=11"); err != nil {
		t.Fatal(err)
	}
	s.Close()
	upgraded, err := OpenPostgres(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	defer upgraded.Close()
	news, err := upgraded.NewsCases(ctx)
	if err != nil || news.ReceiverID != receiver.ID || len(news.Cases) != 0 {
		t.Fatal("upgrade changed receiver or invented evidence", err)
	}
	after, err := upgraded.Snapshot(ctx)
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatal("upgrade rewrote fixture aggregate", err)
	}
	var version int
	if err = upgraded.pool.QueryRow(ctx, "SELECT max(version) FROM mantik_schema").Scan(&version); err != nil || version != 109 {
		t.Fatal(version, err)
	}
	if _, err = upgraded.pool.Exec(ctx, "UPDATE mantik_schema SET sha256='changed' WHERE version=11"); err != nil {
		t.Fatal(err)
	}
	if bad, err := OpenPostgres(ctx, config); err == nil {
		bad.Close()
		t.Fatal("migration tamper admitted")
	}
}
