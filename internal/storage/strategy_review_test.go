package storage

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/account"
	"git.subcult.tv/subculture-collective/mantik/internal/risk"
	"git.subcult.tv/subculture-collective/mantik/internal/strategygate"
)

func strategyReviewStorageContracts(t *testing.T, s CappedBookStore) {
	t.Helper()
	ctx := context.Background()
	gateStorageContracts(t, s.(strategygate.Store))
	portfolioJournalContracts(t, s)
	b, e := s.CappedBook(ctx, "risk-book")
	if e != nil {
		t.Fatal(e)
	}
	roster, e := account.PortfolioRoster(b)
	if e != nil {
		t.Fatal(e)
	}
	at := b.Config.OpenedAt.Add(time.Second)
	d := account.ReviewDeclaration{ID: "review-config", SleeveID: "trend", StrategyID: roster["trend"].StrategyID, GateID: roster["trend"].GateID, Policy: account.ReviewPolicy, Benchmark: "cash", DrawdownBudget: "0.03", BacktestUnavailable: "forward_only_has_no_historical_performance"}
	cmd := account.BookCommand{ID: "review-declare-" + d.ID, Kind: "review_declare", At: at, ReviewDeclaration: &d}
	b, e = s.ApplyCappedBook(ctx, b.Config.ID, b.Version, cmd)
	if e != nil {
		t.Fatal(e)
	}
	in := account.ReviewInput{DeclarationID: d.ID, At: at.Add(time.Second), Mark: account.ReviewMark{Prices: map[string]string{"SPY": "100"}, SourceSHA256: strings.Repeat("c", 64), EvidenceClass: "prospective_market_unqualified", ObservedAt: at.Add(time.Second), ReceivedAt: at.Add(time.Second)}}
	review, e := account.BuildStrategyReview(b, in)
	if e != nil {
		t.Fatal(e)
	}
	reviewCmd := account.BookCommand{ID: "daily-review", Kind: "strategy_review", At: in.At, Review: &review}
	// Both workers computed from the same prefix. Exactly one new write may win;
	// the loser must reload, and owner retirement remains an independent event.
	var wg sync.WaitGroup
	errs := make(chan error, 2)
	wg.Go(func() { _, e := s.ApplyCappedBook(ctx, b.Config.ID, b.Version, reviewCmd); errs <- e })
	owner := account.BookCommand{ID: "owner-retire", Kind: "risk_control", At: in.At, RiskControl: &risk.Control{Action: "retire_strategy", StrategyID: d.StrategyID}}
	wg.Go(func() { _, e := s.ApplyCappedBook(ctx, b.Config.ID, b.Version, owner); errs <- e })
	wg.Wait()
	close(errs)
	success := 0
	for e := range errs {
		if e == nil {
			success++
		} else if !errors.Is(e, account.ErrBookConflict) {
			t.Fatal(e)
		}
	}
	if success != 1 {
		t.Fatal("concurrent review/control not serialized", success)
	}
	b, e = s.CappedBook(ctx, b.Config.ID)
	if e != nil {
		t.Fatal(e)
	}
	hasReview, hasOwner := false, false
	for _, e := range b.Events {
		hasReview = hasReview || e.ID == reviewCmd.ID
		hasOwner = hasOwner || e.ID == owner.ID
	}
	if !hasReview {
		review, e = account.BuildStrategyReview(b, in)
		if e != nil {
			t.Fatal(e)
		}
		reviewCmd.Review = &review
		b, e = s.ApplyCappedBook(ctx, b.Config.ID, b.Version, reviewCmd)
		if e != nil {
			t.Fatal(e)
		}
	}
	if !hasOwner {
		b, e = s.ApplyCappedBook(ctx, b.Config.ID, b.Version, owner)
		if e != nil {
			t.Fatal(e)
		}
	}
	before := b.Version
	b, e = s.ApplyCappedBook(ctx, b.Config.ID, 0, reviewCmd)
	if e != nil || b.Version != before {
		t.Fatal("exact retry", e)
	}
	changed := review
	changed.Action = "grow"
	changed.Scale = "1"
	bad := reviewCmd
	bad.Review = &changed
	if _, e = s.ApplyCappedBook(ctx, b.Config.ID, b.Version, bad); !errors.Is(e, account.ErrBookConflict) {
		t.Fatal("changed retry accepted", e)
	}
	b, e = s.ApplyCappedBook(ctx, b.Config.ID, b.Version, account.BookCommand{ID: "owner-resume", Kind: "risk_control", At: in.At, RiskControl: &risk.Control{Action: "resume_strategy", StrategyID: d.StrategyID}})
	if e != nil {
		t.Fatal(e)
	}
	detached, e := s.CappedBook(ctx, b.Config.ID)
	if e != nil || !reflect.DeepEqual(b, detached) {
		t.Fatal("readback changed evidence", e)
	}
	snap, e := account.SnapshotCapped(detached, in.Mark.Prices, in.At)
	if e != nil || !snap.Risk.Retired[d.StrategyID] || !snap.Risk.Kill {
		t.Fatal("review cleared owner brake", snap, e)
	}
}

func TestMemoryStrategyReviewJournal(t *testing.T) { strategyReviewStorageContracts(t, NewMemory()) }
func TestPostgresContractsStrategyReviewJournal(t *testing.T) {
	p := timerPostgres(t, "strategy_review")
	strategyReviewStorageContracts(t, p)
	ctx := context.Background()
	before, e := p.CappedBook(ctx, "risk-book")
	if e != nil {
		t.Fatal(e)
	}
	reopened, e := OpenPostgres(ctx, p.pool.Config().ConnString())
	if e != nil {
		t.Fatal(e)
	}
	defer reopened.Close()
	after, e := reopened.CappedBook(ctx, "risk-book")
	if e != nil || !reflect.DeepEqual(before, after) {
		t.Fatal("restart changed review journal", e)
	}
	snap, e := account.SnapshotCapped(after, map[string]string{"SPY": "100"}, after.Events[len(after.Events)-1].At)
	if e != nil || !snap.Risk.Retired[after.Events[0].Rebalance.Sleeves[0].StrategyID] {
		t.Fatal("restart lost permanent retirement", snap, e)
	}
}
