package storage

import (
	"context"
	"reflect"
	"testing"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/feedcollector"
	"git.subcult.tv/subculture-collective/mantik/internal/paperstrategy"
)

func TestPostgresContractsOwnedDecisionLockOrder(t *testing.T) {
	s := timerPostgres(t, "decisionlockorder")
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	owner, err := s.NewsCases(ctx)
	if err != nil {
		t.Fatal(err)
	}
	at := time.Now().UTC()
	strategy, err := s.RegisterPaperStrategy(ctx, paperstrategy.Command{ReceiverID: owner.ReceiverID, Definition: strategyDefinition("lock-baseline")}, func() time.Time { return at })
	if err != nil {
		t.Fatal(err)
	}
	reg := feedcollector.Registration{ID: "lock-spy", Provider: "alpaca", Feed: "iex", Symbol: "SPY", EvidenceClass: "prospective_market_unqualified", JournalID: "lock-fixture", AccessRef: "manufactured-fixture", LeaseSeconds: 300}
	if _, err := s.FeedCollectorCommand(ctx, owner.ReceiverID, feedcollector.Command{RequestID: "lock-register", Operation: "register", RegistrationID: reg.ID, Registration: &reg}, func() time.Time { return at }); err != nil {
		t.Fatal(err)
	}
	before, err := s.Snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	// Capital policy and fixture binding acquire state before receiver. Hold their
	// first actual row lock while the real finalizer reaches its state lock. A
	// finalizer that already holds receiver creates a PostgreSQL deadlock here.
	blocker, err := s.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = blocker.Rollback(context.Background()) }()
	var raw []byte
	if err := blocker.QueryRow(ctx, "SELECT payload FROM mantik_state WHERE id='demo' FOR UPDATE").Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var blockerPID int
	if err := blocker.QueryRow(ctx, "SELECT pg_backend_pid()").Scan(&blockerPID); err != nil {
		t.Fatal(err)
	}
	c := PaperDecisionCommand{ID: "lock-decision", ReceiverID: owner.ReceiverID, StrategyID: strategy.Record.Command.Definition.ID, StrategySHA256: strategy.Record.SHA256, RegistrationID: reg.ID}
	type completed struct {
		receipt PaperDecisionReceipt
		err     error
	}
	done := make(chan completed, 1)
	go func() {
		receipt, err := s.FinalizePaperDecision(ctx, c, func() time.Time { return at.Add(time.Second) }, func(PaperDecisionCommand, PaperDecisionBasis, time.Time) (PaperDecisionEvaluation, error) {
			return PaperDecisionEvaluation{Action: "abstain", ReasonCode: "lock_contract_only"}, nil
		})
		done <- completed{receipt, err}
	}()
	deadline := time.NewTimer(8 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	waiting := false
	for !waiting {
		if err := s.pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM pg_stat_activity WHERE datname=current_database() AND $1=ANY(pg_blocking_pids(pid)))`, blockerPID).Scan(&waiting); err != nil {
			t.Fatal(err)
		}
		if waiting {
			break
		}
		select {
		case result := <-done:
			t.Fatal("finalizer did not wait for held state row", result.err)
		case <-deadline.C:
			t.Fatal("finalizer never reached held state row")
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		case <-ticker.C:
		}
	}
	// This must be obtainable immediately while finalization waits for state.
	// A receiver-first finalizer either deadlocks this transaction or is itself
	// selected as the deadlock victim. Both are actual API failures, not mocks.
	var receiver string
	if err := blocker.QueryRow(ctx, "SELECT id FROM mantik_research_receiver WHERE singleton FOR UPDATE").Scan(&receiver); err != nil {
		t.Fatal("capital owner receiver lock", err)
	}
	if receiver != owner.ReceiverID {
		t.Fatal("receiver changed")
	}
	if err := blocker.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case result := <-done:
		if result.err != nil || result.receipt.Duplicate || result.receipt.Record.Command != c {
			t.Fatal("finalization failed after owner commit", result.err)
		}
	case <-ctx.Done():
		t.Fatal("finalization did not complete", ctx.Err())
	}
	after, err := s.Snapshot(ctx)
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatal("lock test changed operational state", err)
	}
}
