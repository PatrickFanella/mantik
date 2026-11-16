package alpaca

import (
	"context"
	"encoding/json"
	"testing"

	"git.subcult.tv/subculture-collective/mantik/internal/account"
)

func TestMLegFullCustodyRefusesCloseBeforePOST(t *testing.T) {
	v, p, _ := newFakeMlegVenue(t)
	o := mlegFixtureOpen()
	o.Order.Qty = "1024"
	for i := range o.Structure.Legs {
		o.Structure.Legs[i].Contracts *= 512
	}
	var err error
	o.Funding, err = account.NewCappedState(account.DefaultCappedConfig("synthetic-capacity", "account-1", account.Margin, o.Chain.AsOf.AddDate(-1, 0, 0)))
	if err != nil {
		t.Fatal(err)
	}
	due, err := o.Funding.Config.DueDeposits(o.Chain.AsOf)
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range due {
		o.Funding, err = account.ApplyCappedCommand(o.Funding, d)
		if err != nil {
			t.Fatal(err)
		}
	}
	opening, err := json.Marshal(o)
	if err != nil {
		t.Fatal(err)
	}
	if err := p.journal.writeOnce(optionLifecycle, opening); err != nil {
		t.Fatal(err)
	}
	req := optionRequest{MLegOrder: o.Order}
	intent, err := json.Marshal(req)
	if err != nil {
		t.Fatal(err)
	}
	if err := p.journal.writeOnce(optionIntentName(req.ClientOrderID), intent); err != nil {
		t.Fatal(err)
	}
	// A coherent retained opening fills one atomic unit at a time. The final
	// observation uses the last available custody slot, leaving no witness slot
	// for a newly transmitted close or cancellation.
	for n := 1; n <= 1024; n++ {
		status := "partially_filled"
		if n == 1024 {
			status = "filled"
		}
		order := mlegResponse(req, n, status)
		body, err := json.Marshal(optionObservation{Request: req, Order: order})
		if err != nil {
			t.Fatal(err)
		}
		if err := p.journal.writeOnce(optionObservationName(n), body); err != nil {
			t.Fatal(err)
		}
		v.orders[req.ClientOrderID] = order
	}
	v.requests[req.ClientOrderID] = req
	if _, n, err := p.optionObservations(); err != nil || n != 1024 {
		t.Fatal("invalid full-custody fixture", n, err)
	}
	out, err := p.CloseMLeg(context.Background(), mlegPlan())
	if err == nil || out.State == "closed" || len(v.posts) != 0 || v.deletes != 0 {
		t.Fatalf("mutation escaped full custody: posts=%d deletes=%d state=%s err=%v", len(v.posts), v.deletes, out.State, err)
	}
}
