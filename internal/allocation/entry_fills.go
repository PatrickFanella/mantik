package allocation

import (
	"context"
	"reflect"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/paperfill"
	"git.subcult.tv/subculture-collective/mantik/internal/workerplan"
)

const EntryFillBindingMethod = "replayed-shared-book-entry-debit-v1"

// ApplyEntryFillBook stages entry debits from a fully replayed shared-liquidity
// book. It accepts no caller amount, snapshot or replacement admission. Every
// order must already have a reservation in the supplied owner ledger. An order
// uses its proposal ID, the exact owner model, USD buy limits, a funded debit
// ceiling and an expiry within that proposal's applicability.
//
// This internal reducer does not attest input ownership, issue admissions or
// write storage. A future execution owner must select actual decision/order/
// quote sources and commit its retained book and resulting ledger atomically.
// All failures return zero results and leave both inputs unchanged. No CLI or
// HTTP route imports this book or ledger.
func ApplyEntryFillBook(ctx context.Context, ledger Ledger, book paperfill.Book) (Ledger, LedgerProjection, []LedgerReceipt, error) {
	fail := func(err error) (Ledger, LedgerProjection, []LedgerReceipt, error) {
		return Ledger{}, LedgerProjection{}, nil, err
	}
	projection, err := ReplayLedger(ctx, ledger)
	if err != nil {
		return fail(err)
	}
	if _, err = paperfill.ReplayBook(ctx, book); err != nil {
		return fail(err)
	}
	model := ledger.Policy.Command.ExecutionModel
	if model == nil || !reflect.DeepEqual(*model, book.Model) {
		return fail(ErrLedgerConflict)
	}
	root, err := paperfill.NewBook(book.Model, book.Orders)
	if err != nil {
		return fail(err)
	}
	orders := map[string]bool{}
	for _, order := range book.Orders {
		if order.Side != "buy" || order.Kind != "limit" || order.Currency != ledger.Policy.Account.Config.Currency {
			return fail(ErrLedgerConflict)
		}
		found := false
		for _, reservation := range projection.Reservations {
			q := reservation.Proposal
			if q.ID != order.ID {
				continue
			}
			found = true
			if q.Symbol != order.Symbol || order.MaxDebitCents > q.NotionalCents || order.ExpiresAt.After(q.ValidUntil) {
				return fail(ErrLedgerConflict)
			}
			// Placement cannot precede the actual reserve decision, even if it is
			// later than policy registration and the proposal's finalization.
			reserved := time.Time{}
			for _, entry := range ledger.Entries {
				if entry.Plan == nil {
					continue
				}
				for _, choice := range entry.Plan.Decisions {
					if choice.Proposal.ID == order.ID && choice.Status == "reserve_plan" {
						reserved = entry.At
					}
				}
			}
			if reserved.IsZero() || order.PlacedAt.Before(reserved) {
				return fail(ErrLedgerConflict)
			}
		}
		if !found {
			return fail(ErrLedgerConflict)
		}
		orders[order.ID] = true
	}
	type staged struct {
		command LedgerCommand
		at      time.Time
	}
	stages := []staged{}
	expected := map[string]staged{}
	for _, entry := range book.Entries {
		if err := ctx.Err(); err != nil {
			return fail(err)
		}
		for _, outcome := range entry.Outcomes {
			fill := outcome.Outcome.Fill
			if fill == nil {
				continue
			}
			// ReplayBook checked the entire prefix and rederived this exact fill,
			// including shared liquidity, model costs and partial-fill debit bounds.
			binding := workerplan.Hash(struct{ Method, BookRootSHA256, FillSHA256 string }{EntryFillBindingMethod, root.SHA256, fill.SHA256})
			id := "entry-fill-" + binding
			effect := Effect{ID: id, SHA256: binding, DecisionID: fill.OrderID, Kind: "fill", AmountCents: fill.DebitCents}
			if !orders[fill.OrderID] || fill.Side != "buy" || fill.DebitCents <= 0 {
				return fail(ErrLedgerConflict)
			}
			stage := staged{LedgerCommand{ID: id, Effect: &effect}, fill.At}
			expected[id] = stage
			stages = append(stages, stage)
		}
	}
	// A changed book root, truncated history, or amount-only prior effect cannot
	// silently relabel earlier debits. Every old entry debit for these decisions
	// must be present with the same derived command and evidence identity.
	for _, entry := range ledger.Entries {
		effect := entry.Command.Effect
		if effect == nil || effect.Kind != "fill" || !orders[effect.DecisionID] {
			continue
		}
		want, ok := expected[effect.ID]
		if !ok || !reflect.DeepEqual(want.command, entry.Command) || !entry.At.Equal(want.at) {
			return fail(ErrLedgerConflict)
		}
	}
	next := ledgerDetach(ledger)
	receipts := []LedgerReceipt{}
	for _, stage := range stages {
		var receipt LedgerReceipt
		next, projection, receipt, err = AppendLedger(ctx, next, stage.command, func() time.Time { return stage.at })
		if err != nil {
			return fail(err)
		}
		receipts = append(receipts, receipt)
	}
	projection, err = ReplayLedger(ctx, next)
	if err != nil {
		return fail(err)
	}
	return next, projection, receipts, nil
}
