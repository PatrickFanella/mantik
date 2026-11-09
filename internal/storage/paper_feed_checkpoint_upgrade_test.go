package storage

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/feedcollector"
	"git.subcult.tv/subculture-collective/mantik/internal/paperstrategy"
	"git.subcult.tv/subculture-collective/mantik/internal/workerplan"
)

func TestPostgresContractsPaperFeedCheckpointUpgrade(t *testing.T) {
	p := timerPostgres(t, "paperfeedupgrade")
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	at := time.Date(2026, 11, 9, 15, 0, 0, 100, time.UTC)
	feed, err := p.FeedEvidence(ctx)
	if err != nil {
		t.Fatal(err)
	}
	owner := feed.ReceiverID
	def := strategyDefinition("checkpoint-upgrade")
	def.InputEvidenceClass = "prospective_market_unqualified"
	strategy, err := p.RegisterPaperStrategy(ctx, paperstrategy.Command{ReceiverID: owner, Definition: def}, func() time.Time { return at })
	if err != nil {
		t.Fatal(err)
	}
	reg := feedcollector.Registration{ID: "upgrade-feed", Provider: "alpaca", Feed: "iex", Symbol: "SPY", EvidenceClass: def.InputEvidenceClass, JournalID: "upgrade-journal", AccessRef: "fixture-only", LeaseSeconds: 300}
	if _, err := p.FeedCollectorCommand(ctx, owner, feedcollector.Command{RequestID: "register", Operation: "register", RegistrationID: reg.ID, Registration: &reg}, func() time.Time { return at }); err != nil {
		t.Fatal(err)
	}
	de := func(PaperDecisionCommand, PaperDecisionBasis, time.Time) (PaperDecisionEvaluation, error) {
		return PaperDecisionEvaluation{Action: "abstain", ReasonCode: "upgrade_fixture"}, nil
	}
	oe := func(PaperOutcomeBasis, time.Time) (PaperOutcomeEvaluation, error) {
		return PaperOutcomeEvaluation{Status: "not_applicable", ReasonCode: "upgrade_fixture"}, nil
	}
	command := PaperDecisionCommand{ID: "original", ReceiverID: owner, StrategyID: def.ID, StrategySHA256: strategy.Record.SHA256, RegistrationID: reg.ID}
	decision, err := p.FinalizePaperDecision(ctx, command, func() time.Time { return at }, de)
	if err != nil {
		t.Fatal(err)
	}
	at = at.Add(time.Nanosecond)
	check := func(FeedEvidenceSnapshot, time.Time) error { return nil }
	archive, err := p.ArchiveFeed(ctx, owner, func() time.Time { return at }, check)
	if err != nil {
		t.Fatal(err)
	}
	var original retainedPaperDecision
	var raw []byte
	if err := p.pool.QueryRow(ctx, "SELECT payload FROM mantik_paper_decisions WHERE id=$1", command.ID).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &original); err != nil {
		t.Fatal(err)
	}
	dropFeedRetentionSchema(t, p)
	// Construct the exact predecessor, retaining original source and paper bytes.
	for _, sql := range []string{
		"DROP TRIGGER mantik_paper_decision_feed_cut ON mantik_paper_decisions",
		"DROP TRIGGER mantik_paper_outcome_feed_cut ON mantik_paper_outcomes",
		"DROP FUNCTION mantik_fence_paper_feed_cut()",
		"DROP TABLE mantik_paper_feed_checkpoints",
		"DELETE FROM mantik_schema WHERE version>=103",
	} {
		if _, err := p.pool.Exec(ctx, sql); err != nil {
			t.Fatal(err)
		}
	}
	writer, err := p.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = writer.Rollback(context.Background()) }()
	if _, err := writer.Exec(ctx, "SELECT singleton FROM mantik_feed_collectors WHERE singleton FOR UPDATE"); err != nil {
		t.Fatal(err)
	}
	var writerPID int
	if err := writer.QueryRow(ctx, "SELECT pg_backend_pid()").Scan(&writerPID); err != nil {
		t.Fatal(err)
	}
	upgrade := make(chan error, 1)
	go func() { upgrade <- p.migrate(ctx) }()
	// Observe the real lock wait before letting the older writer continue to its
	// paper table. Acquiring DDL locks first would create the opposite lock order.
	deadline := time.Now().Add(5 * time.Second)
	for {
		var blocked bool
		if err := p.pool.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE datname=current_database() AND $1=ANY(pg_blocking_pids(pid)) AND query LIKE 'SELECT singleton FROM mantik_feed_collectors%')", writerPID).Scan(&blocked); err != nil {
			t.Fatal(err)
		}
		if blocked {
			break
		}
		select {
		case err := <-upgrade:
			t.Fatal("migration did not wait for source owner", err)
		default:
		}
		if time.Now().After(deadline) {
			t.Fatal("migration lock wait was not observed")
		}
		time.Sleep(10 * time.Millisecond)
	}
	later := clone(original)
	later.Record.Command.ID = "old-writer"
	later.Record.FinalizedAt = at.Add(time.Nanosecond)
	later.Record.SHA256 = ""
	later.Record.SHA256 = workerplan.Hash(later.Record)
	raw, _ = json.Marshal(later)
	if _, err := writer.Exec(ctx, "INSERT INTO mantik_paper_decisions(id,payload) VALUES($1,$2)", later.Record.Command.ID, raw); err != nil {
		t.Fatal(err)
	}
	if err := writer.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if err := <-upgrade; err != nil {
		t.Fatal("upgrade deadlocked with an older source writer", err)
	}
	noClock := func() time.Time { t.Fatal("unchanged source retry sampled time"); return time.Time{} }
	receipt, err := p.ArchivePaperFeed(ctx, owner, noClock, check, de, oe)
	if err != nil || !receipt.Duplicate || receipt.Header != archive.Header || receipt.PaperCustody == nil || receipt.PaperCustody.Header.Decisions != 2 || !receipt.PaperCustody.Header.CutAt.Equal(later.Record.FinalizedAt) {
		t.Fatal("upgrade omitted predecessor paper records", receipt, err)
	}
	want, err := p.PaperFeedCheckpoint(ctx, 1, de, oe)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.pool.Exec(ctx, "DELETE FROM mantik_schema WHERE version>=103"); err != nil {
		t.Fatal(err)
	}
	if err := p.migrate(ctx); err != nil {
		t.Fatal("populated checkpoint DDL replay", err)
	}
	got, err := p.PaperFeedCheckpoint(ctx, 1, de, oe)
	if err != nil || !reflect.DeepEqual(want, got) {
		t.Fatal("DDL replay rewrote checkpoint", err)
	}
	sources, err := p.PaperDecisionSources(ctx, command.ID, de)
	if err != nil || !reflect.DeepEqual(sources.Decision, decision.Record) || !reflect.DeepEqual(sources.Basis, original.Basis) {
		t.Fatal("upgrade rewrote paper envelope", err)
	}
	if _, err := p.pool.Exec(ctx, "UPDATE mantik_schema SET sha256=$1 WHERE version=103", strings.Repeat("0", 64)); err != nil {
		t.Fatal(err)
	}
	if err := p.migrate(ctx); err == nil || !strings.Contains(err.Error(), "checksum mismatch") {
		t.Fatal("checkpoint migration checksum ignored", err)
	}
}
