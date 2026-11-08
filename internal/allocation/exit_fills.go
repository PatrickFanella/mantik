package allocation

import (
	"context"
	"reflect"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/paperfill"
	"git.subcult.tv/subculture-collective/mantik/internal/workerplan"
)

const ExitFillBindingMethod = "replayed-shared-book-exit-proceeds-v1"

// ApplyExitFillBook stages closes from replayed sell fills. Order IDs in the
// exit book select the same proposal IDs used by the entry book and ledger.
// Entry orders must be terminal before exit placement; every entry debit must
// already be committed with its exact book binding. Cost basis includes entry
// costs and is allocated by cumulative whole-share average cost, rounding down
// until the final share receives the remainder. No caller amount is accepted.
//
// This internal reducer neither attests source ownership nor writes storage or
// grants admission. The execution owner must select actual orders and evidence,
// enforce applicable exits, and atomically commit its books and resulting ledger.
func ApplyExitFillBook(ctx context.Context, ledger Ledger, entries, exits paperfill.Book) (Ledger, LedgerProjection, []LedgerReceipt, error) {
	return ApplyExitFillBooks(ctx, ledger, entries, []paperfill.Book{exits})
}

// ApplyExitFillBooks is ApplyExitFillBook over a sequence of exit books, such
// as an exit that expired with shares left and the replacement that sells the
// rest. A later book may sell an order again only after that order is terminal
// in every earlier book, may not be placed before it ended there, and may sell
// at most what the earlier books left. Cost basis continues across the books,
// and each fill binds its own book's root, so the first book stages exactly
// what ApplyExitFillBook stages for it alone. Every close the ledger holds for
// these positions must come from these books.
func ApplyExitFillBooks(ctx context.Context, ledger Ledger, entries paperfill.Book, exits []paperfill.Book) (Ledger, LedgerProjection, []LedgerReceipt, error) {
	fail := func(err error) (Ledger, LedgerProjection, []LedgerReceipt, error) {
		return Ledger{}, LedgerProjection{}, nil, err
	}
	if len(exits) == 0 {
		return fail(ErrLedgerConflict)
	}
	projection, err := ReplayLedger(ctx, ledger)
	if err != nil {
		return fail(err)
	}
	entryProjection, err := paperfill.ReplayBook(ctx, entries)
	if err != nil {
		return fail(err)
	}
	// Applying the entry book may only replay already committed debits. A close
	// cannot conceal a missing entry posting or settle a new entry implicitly.
	checked, _, _, err := ApplyEntryFillBook(ctx, ledger, entries)
	if err != nil {
		return fail(err)
	}
	if !reflect.DeepEqual(checked, ledger) {
		return fail(ErrLedgerConflict)
	}
	type position struct{ quantity, cost, closed int64 }
	positions := map[string]*position{}
	// ended and working describe each order in the earlier books.
	ended, working := map[string]time.Time{}, map[string]bool{}
	type staged struct {
		command LedgerCommand
		fill    paperfill.Fill
	}
	stages := []staged{}
	expected := map[string]staged{}
	for _, exits := range exits {
		exitProjection, err := paperfill.ReplayBook(ctx, exits)
		if err != nil {
			return fail(err)
		}
		if !reflect.DeepEqual(entries.Model, exits.Model) {
			return fail(ErrLedgerConflict)
		}
		root, err := paperfill.RootBook(exits)
		if err != nil {
			return fail(err)
		}
		for _, exit := range exits.Orders {
			matched := false
			for i, entry := range entries.Orders {
				if exit.ID != entry.ID {
					continue
				}
				p := entryProjection.Orders[i].Projection
				if exit.Side != "sell" || exit.Symbol != entry.Symbol || exit.Currency != entry.Currency || exit.Mode != entry.Mode || p.Status == "working" || p.FilledQuantity == 0 || exit.PlacedAt.Before(p.LastClock) {
					return fail(ErrLedgerConflict)
				}
				pos := positions[exit.ID]
				if pos == nil {
					pos = &position{quantity: p.FilledQuantity, cost: p.DebitCents}
					positions[exit.ID] = pos
				}
				if end, earlier := ended[exit.ID]; earlier && (working[exit.ID] || exit.PlacedAt.Before(end)) {
					return fail(ErrLedgerConflict)
				}
				if exit.Quantity > pos.quantity-pos.closed {
					return fail(ErrLedgerConflict)
				}
				matched = true
			}
			if !matched {
				return fail(ErrLedgerConflict)
			}
		}
		for _, entry := range exits.Entries {
			if err := ctx.Err(); err != nil {
				return fail(err)
			}
			for _, out := range entry.Outcomes {
				fill := out.Outcome.Fill
				if fill == nil {
					continue
				}
				p := positions[fill.OrderID]
				if p == nil || fill.Side != "sell" || fill.Quantity <= 0 || fill.Quantity > p.quantity-p.closed {
					return fail(ErrLedgerConflict)
				}
				// Division first avoids cost*quantity overflow. Both remainder and
				// quantity are bounded by paperfill.MaxQuantity (one million).
				basis := func(q int64) int64 { return p.cost/p.quantity*q + (p.cost%p.quantity)*q/p.quantity }
				amount := basis(p.closed+fill.Quantity) - basis(p.closed)
				p.closed += fill.Quantity
				binding := workerplan.Hash(struct{ Method, EntryBookSHA256, ExitBookRootSHA256, FillSHA256 string }{ExitFillBindingMethod, entries.SHA256, root.SHA256, fill.SHA256})
				id := "exit-fill-" + binding
				effect := Effect{ID: id, SHA256: binding, DecisionID: fill.OrderID, Kind: "close", AmountCents: amount, ProceedsCents: fill.NetProceedsCents}
				stage := staged{LedgerCommand{ID: id, Effect: &effect}, *fill}
				expected[id] = stage
				stages = append(stages, stage)
			}
		}
		for _, o := range exitProjection.Orders {
			ended[o.OrderID], working[o.OrderID] = o.Projection.LastClock, o.Projection.Status == "working"
		}
	}
	// Reject amount-only closes, alternate roots, truncated histories and an
	// exit book outside the sequence that would reuse a position's quantity.
	for _, entry := range ledger.Entries {
		effect := entry.Command.Effect
		if effect == nil || effect.Kind != "close" || positions[effect.DecisionID] == nil {
			continue
		}
		want, ok := expected[effect.ID]
		if !ok || !reflect.DeepEqual(want.command, entry.Command) || !entry.At.Equal(want.fill.At) {
			return fail(ErrLedgerConflict)
		}
	}
	next := ledgerDetach(ledger)
	receipts := []LedgerReceipt{}
	for _, stage := range stages {
		var receipt LedgerReceipt
		next, projection, receipt, err = AppendLedger(ctx, next, stage.command, func() time.Time { return stage.fill.At })
		if err != nil {
			return fail(err)
		}
		receipts = append(receipts, receipt)
	}
	return next, projection, receipts, nil
}
