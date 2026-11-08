package allocation

import (
	"context"
	"reflect"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/paperfill"
	"git.subcult.tv/subculture-collective/mantik/internal/workerplan"
)

const ClosedPositionReleaseMethod = "replayed-fully-closed-position-release-v1"

// ApplyClosedPositionReleases stages the unused reservation only after all entry
// debits and exit closes are already posted with exact replayed book bindings.
// A terminal entry and a complete exit of its actual filled quantity are both
// required. Partial exits, missing quotes and unresolved effects cannot release
// protective funding. The amount is original funding less actual entry debits.
//
// This internal reducer does not attest source ownership, issue admission or
// write storage. The execution owner must retain its actual orders/protection
// state and atomically commit books and ledger in event order. Failures return
// zero results and leave all inputs unchanged; no public writer imports them.
func ApplyClosedPositionReleases(ctx context.Context, ledger Ledger, entries, exits paperfill.Book) (Ledger, LedgerProjection, []LedgerReceipt, error) {
	return ApplyClosedPositionReleaseBooks(ctx, ledger, entries, []paperfill.Book{exits})
}

// ApplyClosedPositionReleaseBooks is ApplyClosedPositionReleases over the
// sequence of exit books ApplyExitFillBooks accepts. A position is complete
// when its order is filled in the last book that holds it and the books
// together sold its whole filled quantity. The release binds the book and
// entry that completed the close, so a position closed by one book releases
// exactly as ApplyClosedPositionReleases releases it.
func ApplyClosedPositionReleaseBooks(ctx context.Context, ledger Ledger, entries paperfill.Book, exits []paperfill.Book) (Ledger, LedgerProjection, []LedgerReceipt, error) {
	fail := func(err error) (Ledger, LedgerProjection, []LedgerReceipt, error) {
		return Ledger{}, LedgerProjection{}, nil, err
	}
	projection, err := ReplayLedger(ctx, ledger)
	if err != nil {
		return fail(err)
	}
	entryProjection, err := paperfill.ReplayBook(ctx, entries)
	if err != nil {
		return fail(err)
	}
	checked, _, _, err := ApplyExitFillBooks(ctx, ledger, entries, exits)
	if err != nil {
		return fail(err)
	}
	// This verifies entry debits too. A release never silently posts a missing
	// debit/close; they must already have succeeded through their own owner path.
	if !reflect.DeepEqual(checked, ledger) {
		return fail(ErrLedgerConflict)
	}
	sold, status := map[string]int64{}, map[string]string{}
	orders := map[string]bool{}
	roots := make([]paperfill.Book, len(exits))
	for i, book := range exits {
		exitProjection, err := paperfill.ReplayBook(ctx, book)
		if err != nil {
			return fail(err)
		}
		if roots[i], err = paperfill.RootBook(book); err != nil {
			return fail(err)
		}
		for _, exit := range exitProjection.Orders {
			orders[exit.OrderID] = true
			sold[exit.OrderID] += exit.Projection.FilledQuantity
			status[exit.OrderID] = exit.Projection.Status
		}
	}
	type candidate struct{ quantity, amount int64 }
	eligible := map[string]candidate{}
	for _, entry := range entryProjection.Orders {
		ep := entry.Projection
		if !orders[entry.OrderID] || ep.Status == "working" || ep.FilledQuantity == 0 || sold[entry.OrderID] != ep.FilledQuantity || status[entry.OrderID] != "filled" {
			continue
		}
		for _, r := range projection.Reservations {
			if r.Proposal.ID != entry.OrderID {
				continue
			}
			if r.Unknown || r.FilledCents != ep.DebitCents || r.ClosedCostCents != r.FilledCents {
				return fail(ErrLedgerConflict)
			}
			amount := r.Proposal.NotionalCents - r.FilledCents
			if amount < 0 {
				return fail(ErrLedgerConflict)
			}
			if amount > 0 {
				eligible[entry.OrderID] = candidate{ep.FilledQuantity, amount}
			}
		}
	}
	type staged struct {
		command LedgerCommand
		at      time.Time
	}
	stages := []staged{}
	expected := map[string]staged{}
	closed := map[string]int64{}
	for i, book := range exits {
		for _, entry := range book.Entries {
			if err := ctx.Err(); err != nil {
				return fail(err)
			}
			for _, out := range entry.Outcomes {
				fill := out.Outcome.Fill
				if fill == nil {
					continue
				}
				target, ok := eligible[out.OrderID]
				if !ok {
					continue
				}
				closed[out.OrderID] += fill.Quantity
				if closed[out.OrderID] != target.quantity {
					continue
				}
				// The first complete close is stable when unrelated exit observations are
				// appended. Bind the full terminal entry book and the exit root/prefix.
				binding := workerplan.Hash(struct{ Method, EntryBookSHA256, ExitBookRootSHA256, ClosingEntrySHA256, OrderID string }{ClosedPositionReleaseMethod, entries.SHA256, roots[i].SHA256, entry.SHA256, out.OrderID})
				id := "closed-release-" + binding
				effect := Effect{ID: id, SHA256: binding, DecisionID: out.OrderID, Kind: "release", AmountCents: target.amount}
				stage := staged{LedgerCommand{ID: id, Effect: &effect}, fill.At}
				expected[id] = stage
				stages = append(stages, stage)
			}
		}
	}
	// Reject amount-only, early, relabelled or truncated releases for the selected
	// exit orders. Independent orders in the same shared entry book stay separate.
	for _, entry := range ledger.Entries {
		effect := entry.Command.Effect
		if effect == nil || effect.Kind != "release" || !orders[effect.DecisionID] {
			continue
		}
		want, ok := expected[effect.ID]
		if !ok || !reflect.DeepEqual(want.command, entry.Command) || !want.at.Equal(entry.At) {
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
