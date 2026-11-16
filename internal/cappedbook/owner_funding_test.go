package cappedbook

import (
	"context"
	"fmt"
	"testing"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/account"
)

func TestOwnerReceiptFundingNeverCreditsMissedSlots(t *testing.T) {
	for _, receipt := range []bool{false, true} {
		for _, operation := range []string{"Fund", "Rebalance", "Submit"} {
			t.Run(fmt.Sprintf("receipt=%t/%s", receipt, operation), func(t *testing.T) {
				ctx := context.Background()
				c, broker, req, marks := portfolioFixture(t, account.OwnerReceipts)
				initial, err := c.load(ctx)
				if err != nil {
					t.Fatal(err)
				}
				through := initial.Config.OpenedAt.AddDate(0, 0, 48)
				due, err := initial.Config.DueDeposits(through)
				if err != nil || len(due) < 3 {
					t.Fatal(due, err)
				}
				wantCash, wantDeposits := "2000.000000000", 0
				if receipt {
					a := account.OwnerBookAction{ExpectedVersion: initial.Version, Action: "deposit", Amount: "500", Source: "isolated-receipt", EffectiveAt: due[0].EffectiveAt.Add(time.Hour)}
					a.Confirmation = a.ConfirmationPin(c.BookID, due[0].ID)
					d := account.BookCommand{ID: due[0].ID, Kind: "deposit", Amount: a.Amount, At: a.EffectiveAt, EffectiveAt: a.EffectiveAt, OwnerAction: &a}
					initial, err = c.Store.ApplyCappedBook(ctx, c.BookID, initial.Version, d)
					if err != nil {
						t.Fatal(err)
					}
					// Restart retry of the original receipt does not invent another flow.
					if _, err = c.Store.ApplyCappedBook(ctx, c.BookID, 0, d); err != nil {
						t.Fatal(err)
					}
					wantCash, wantDeposits = "2500.000000000", 1
				}
				restarted := Client{Store: c.Store, Broker: c.Broker, BookID: c.BookID}
				req.At = through
				switch operation {
				case "Fund":
					if _, err = restarted.Fund(ctx, through); err != nil {
						t.Fatal(err)
					}
				case "Rebalance":
					if _, err = restarted.Rebalance(ctx, req, marks); err != nil {
						t.Fatal(err)
					}
				case "Submit":
					req.Sleeves[0].Minimum = "1" // retained fixture gate minimum
					// Retain the sized plan directly so Submit is the first client method
					// called at this later clock. Its internal Fund must not create cash.
					b, e := c.Store.ApplyCappedBook(ctx, c.BookID, initial.Version, account.BookCommand{ID: req.ID, Kind: "rebalance", At: req.At, Rebalance: &req, Marks: marks})
					if e != nil {
						t.Fatal(e)
					}
					snap, e := account.SnapshotCapped(b, marks, through)
					if e != nil || snap.Distribution == nil || len(snap.Distribution.Orders) == 0 {
						t.Fatal(snap, e)
					}
					if _, err = restarted.Submit(ctx, portfolioOrder(snap.Distribution.Orders[0]), marks); err != nil {
						t.Fatal(err)
					}
					if broker.posts != 1 {
						t.Fatal("fixture submit did not exercise broker boundary", broker.posts)
					}
				}
				b, err := c.Store.CappedBook(ctx, c.BookID)
				if err != nil {
					t.Fatal(err)
				}
				v, err := account.ReadOwnerBook(b, through)
				if err != nil || v.Cash != wantCash || len(v.Deposits) != wantDeposits {
					t.Fatal("missed slots acquired spendable cash", v, err)
				}
				before := b.Version
				b, err = restarted.Fund(ctx, through.AddDate(0, 0, 14))
				if err != nil || b.Version != before {
					t.Fatal("later/restarted Fund created cash", b.Version, before, err)
				}
			})
		}
	}
}
