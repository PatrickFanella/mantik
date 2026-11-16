package mlegpaper

import (
	"context"
	"strings"
	"testing"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/account"
)

func TestLocalAdmissionPreservesOwnerReceiptFunding(t *testing.T) {
	o := admissionFixture(t, "iron_condor", true)
	o.Funding.Config.FundingMode = account.OwnerReceipts
	o.Funding.Events = nil
	o.Funding.Version = 0
	at := o.Chain.AsOf
	// Elapsed schedule slots alone cannot lift the $2000 margin refusal.
	if err := admit(context.Background(), o, at); err == nil || !strings.Contains(err.Error(), "option_cash_structure_not_allowed") {
		t.Fatal("unreceived deposits authorized a spread", err)
	}
	due, err := o.Funding.Config.DueDeposits(at)
	if err != nil || len(due) == 0 {
		t.Fatal(due, err)
	}
	deposit := due[0]
	deposit.At = at
	deposit.EffectiveAt = deposit.EffectiveAt.Add(time.Hour)
	for _, confirmed := range []bool{false, true} {
		t.Run(map[bool]string{false: "unconfirmed", true: "fixture_receipt"}[confirmed], func(t *testing.T) {
			input := o
			event := deposit
			if confirmed {
				a := account.OwnerBookAction{Action: "deposit", Amount: "500", Source: "synthetic-mleg-test-receipt", EffectiveAt: event.EffectiveAt}
				a.Confirmation = a.ConfirmationPin(input.Funding.Config.ID, event.ID)
				event.OwnerAction = &a
			}
			// Replay supplied input directly: an unconfirmed raw event must fail too.
			input.Funding.Events = []account.BookCommand{event}
			input.Funding.Version = 1
			err := admit(context.Background(), input, at)
			if (err == nil) != confirmed {
				t.Fatal("owner receipt admission disagrees with funding replay", err)
			}
		})
	}
}
