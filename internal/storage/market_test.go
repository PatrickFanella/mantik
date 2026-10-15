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
	if _, err := s.pool.Exec(ctx, "ALTER TABLE mantik_state DROP CONSTRAINT mantik_state_requires_input_contract; DROP TABLE mantik_feed_evidence; DROP TABLE mantik_feed_collectors; DELETE FROM mantik_schema WHERE version>=6; UPDATE mantik_state SET payload=payload #- '{routing,input_contract}'"); err != nil {
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
