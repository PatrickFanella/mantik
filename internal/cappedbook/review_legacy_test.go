package cappedbook

import (
	"context"
	"encoding/json"
	"git.subcult.tv/subculture-collective/mantik/internal/account"
	"git.subcult.tv/subculture-collective/mantik/internal/allocation"
	"git.subcult.tv/subculture-collective/mantik/internal/risk"
	"testing"
	"time"
)

func TestPortfolioLegacyPausedReductionReplay(t *testing.T) {
	ctx := context.Background()
	c, b, req, marks := portfolioFixture(t)
	first, e := c.Rebalance(ctx, req, marks)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = c.Submit(ctx, portfolioOrder(first.Orders[0]), marks); e != nil {
		t.Fatal(e)
	}
	o := first.Orders[0]
	b.fills = []json.RawMessage{rawFill("legacy-full", o.ID, o.Quantity, req.At.Add(time.Second))}
	venue := b.orders[o.ID]
	venue.FilledQty = o.Quantity
	venue.Status = "filled"
	b.orders[o.ID] = venue
	if _, e = c.Sync(ctx, req.At.Add(2*time.Second), marks); e != nil {
		t.Fatal(e)
	}
	if _, e = c.Control(ctx, "pause", req.At.Add(3*time.Second), risk.Control{Action: "pause_strategy", StrategyID: req.Sleeves[0].StrategyID}); e != nil {
		t.Fatal(e)
	}
	req.ID = "paused-reduction"
	req.At = req.At.Add(4 * time.Second)
	req.Targets = []allocation.SleeveTarget{{SleeveID: "trend", Decision: allocation.TargetDecision{Targets: []allocation.WeightTarget{{Asset: "SPY", Weight: "0.5"}}}}}
	dist, e := c.Rebalance(ctx, req, marks)
	if e != nil {
		t.Fatal(e)
	}
	if len(dist.Orders) != 1 || !dist.Orders[0].ReduceOnly {
		t.Fatal(dist)
	}
	if _, e = c.Submit(ctx, portfolioOrder(dist.Orders[0]), marks); e != nil {
		t.Fatal(e)
	}
	book, e := c.load(ctx)
	if e != nil {
		t.Fatal(e)
	}
	raw, e := json.Marshal(book)
	if e != nil {
		t.Fatal(e)
	}
	var restored account.CappedState
	if e = json.Unmarshal(raw, &restored); e != nil {
		t.Fatal(e)
	}
	snap, e := account.SnapshotCapped(restored, marks, req.At)
	if e != nil {
		t.Fatal("legacy paused reduction no longer replays", e)
	}
	if snap.Distribution == nil || snap.Distribution.InputSHA256 != dist.InputSHA256 || len(snap.Pending) != 1 || !snap.Pending[0].ReduceOnly {
		t.Fatal(snap)
	}
	retried, e := c.Rebalance(ctx, req, marks)
	if e != nil || retried.InputSHA256 != dist.InputSHA256 {
		t.Fatal("changed legacy retry", retried, e)
	}
}
