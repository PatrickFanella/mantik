package storage

import (
	"context"
	"reflect"
	"testing"

	"git.subcult.tv/subculture-collective/mantik/internal/workflow"
)

func TestPostgresContractsMarketUpgrade(t *testing.T) {
	s := timerPostgres(t, "marketupgrade")
	url := s.pool.Config().ConnString()
	ctx := context.Background()
	quote := workflow.Fixtures()[1]
	if _, err := s.Ingest(ctx, quote); err != nil {
		t.Fatal(err)
	}
	drainFixtureActions(t, s, quote.ReceivedAt)
	before, err := s.Snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	// Simulate an actual v5 payload and metadata without changing old checksums.
	if _, err := s.pool.Exec(ctx, "DROP TABLE mantik_owned_allocations; DROP TABLE mantik_paper_decisions; DROP TABLE mantik_paper_strategies; DROP TABLE mantik_allocation_policies; ALTER TABLE mantik_candidate_registrations DROP COLUMN generation_link_sha256 CASCADE; DROP TABLE mantik_generated_candidate_links; DROP TABLE mantik_candidate_generation_outcomes; DROP TABLE mantik_candidate_generation_datasets; DROP TABLE mantik_candidate_generation_intents; DROP TABLE mantik_candidate_worker_outcomes; DROP TABLE mantik_candidate_worker_intents; DROP TABLE mantik_candidate_datasets; DROP TABLE mantik_candidate_registrations; DROP TABLE mantik_fixture_capital; DROP TABLE mantik_paper_ledger_rehearsals; DROP TABLE mantik_paper_accounts; DROP TABLE mantik_switchyard_capture_admissions; DROP TABLE mantik_switchyard_extraction_captures; DROP TABLE mantik_local_capture_admissions; DROP TABLE mantik_local_extraction_captures; DROP TABLE mantik_news_extraction_admissions; DROP TABLE mantik_news_extractions; ALTER TABLE mantik_state DROP CONSTRAINT mantik_state_requires_input_contract; DROP TABLE mantik_external_trial_attempt_outputs; DROP TABLE mantik_external_trial_attempts; DROP TABLE mantik_external_trial_job_inputs; DROP TABLE mantik_external_trial_jobs; DROP TABLE mantik_trial_run_artifacts; DROP TABLE mantik_trial_runs; DROP TABLE mantik_trial_attempts; DROP TABLE mantik_hypothesis_trials; DROP TABLE mantik_hypothesis_publications; DROP TABLE mantik_news_reviews; DROP TABLE mantik_news_cases; DROP TABLE mantik_feed_evidence; DROP TABLE mantik_feed_collectors; DELETE FROM mantik_schema WHERE version>=6; UPDATE mantik_state SET payload=payload #- '{routing,input_contract}'"); err != nil {
		t.Fatal(err)
	}
	s.Close()
	s, err = OpenPostgres(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	after, err := s.Snapshot(ctx)
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatal("v5 upgrade changed original state")
	}
	marketRoutingContracts(t, s)
	retained, err := s.Snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	s.Close()
	s, err = OpenPostgres(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	reopened, err := s.Snapshot(ctx)
	if err != nil || !reflect.DeepEqual(retained, reopened) {
		t.Fatal("restart lost market attribution or original execution state")
	}
	if _, err := s.pool.Exec(ctx, "UPDATE mantik_state SET payload=payload #- '{routing,input_contract}'"); err == nil {
		t.Fatal("old writer could discard market contract")
	}
	if _, err := s.pool.Exec(ctx, "UPDATE mantik_schema SET sha256='changed' WHERE version=6"); err != nil {
		t.Fatal(err)
	}
	if bad, err := OpenPostgres(ctx, url); err == nil {
		bad.Close()
		t.Fatal("changed market checksum admitted")
	}
}
