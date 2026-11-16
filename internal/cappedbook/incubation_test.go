package cappedbook

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/account"
	"git.subcult.tv/subculture-collective/mantik/internal/allocation"
	"git.subcult.tv/subculture-collective/mantik/internal/executioncost"
)

func TestIncubationPlanCannotAttachRetrospectivelyThroughObservation(t *testing.T) {
	ctx := context.Background()
	c, b, req, marks := portfolioFixture(t)
	d, e := c.Rebalance(ctx, req, marks)
	if e != nil {
		t.Fatal(e)
	}
	o := portfolioOrder(d.Orders[0])
	if _, e = c.Submit(ctx, o, marks); e != nil {
		t.Fatal(e)
	}
	state, e := c.Store.CappedBook(ctx, c.BookID)
	if e != nil {
		t.Fatal(e)
	}
	p := account.IncubationPlan{Rebalance: allocation.RebalanceRequest{ID: req.ID}, Costs: map[string]executioncost.Model{"trend": executioncost.Default()}, Quotes: map[string]account.IncubationQuote{"SPY": {Bid: "99", Ask: "99", BidQuantity: "100", AskQuantity: "100", SourceSHA256: strings.Repeat("c", 64)}}}
	if _, e = c.Store.ApplyCappedBook(ctx, c.BookID, state.Version, account.BookCommand{ID: "late-plan", Kind: "risk_observe", At: req.At, Marks: marks, Incubation: &p}); e == nil {
		t.Fatal("observation retrospectively attached an invalid model plan")
	}
	b.fills = []json.RawMessage{rawFill("entry", o.ID, "1", req.At.Add(time.Minute))}
	v := b.orders[o.ID]
	v.FilledQty, v.Status = "1", "filled"
	b.orders[o.ID] = v
	if _, e = c.Sync(ctx, req.At.Add(2*time.Minute), marks); e != nil {
		t.Fatal(e)
	}
	state, e = c.Store.CappedBook(ctx, c.BookID)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = account.IncubationReturns(state, marks, req.At.Add(2*time.Minute)); e == nil {
		t.Fatal("execution outside incubation produced an incubation return")
	}
}
