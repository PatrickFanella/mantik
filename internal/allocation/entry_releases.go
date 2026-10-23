package allocation

import (
	"context"
	"reflect"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/paperfill"
	"git.subcult.tv/subculture-collective/mantik/internal/workerplan"
)

const UnfilledEntryReleaseMethod = "replayed-unfilled-entry-terminal-release-v1"

// ApplyUnfilledEntryReleases binds an unspent reservation to an actual replayed
// cancellation or expiry with zero filled shares. Missing quotes, elapsed wall
// time, working orders and filled positions never imply cancellation or release.
// Any filled order keeps its entire remaining hold, including protective funding.
//
// All entry debits in this book must already be committed with exact bindings.
// This internal reducer neither proves source ownership nor issues admission or
// writes storage. An execution owner must commit its book and ledger atomically
// in event order. Failures return zero results without changing either input.
func ApplyUnfilledEntryReleases(ctx context.Context, ledger Ledger, book paperfill.Book) (Ledger, LedgerProjection, []LedgerReceipt, error) {
	fail := func(err error) (Ledger, LedgerProjection, []LedgerReceipt, error) {
		return Ledger{}, LedgerProjection{}, nil, err
	}
	projection, err := ReplayLedger(ctx, ledger)
	if err != nil {
		return fail(err)
	}
	replay, err := paperfill.ReplayBook(ctx, book)
	if err != nil {
		return fail(err)
	}
	checked, _, _, err := ApplyEntryFillBook(ctx, ledger, book)
	if err != nil {
		return fail(err)
	}
	if !reflect.DeepEqual(checked, ledger) {
		return fail(ErrLedgerConflict)
	}
	root, err := paperfill.RootBook(book)
	if err != nil {
		return fail(err)
	}
	orders := map[string]bool{}
	eligible := map[string]Reservation{}
	for _, order := range replay.Orders {
		orders[order.OrderID] = true
		p := order.Projection
		if p.FilledQuantity != 0 || (p.Status != "cancelled" && p.Status != "expired") {
			continue
		}
		for _, r := range projection.Reservations {
			if r.Proposal.ID == order.OrderID {
				if r.FilledCents != 0 || r.Unknown {
					return fail(ErrLedgerConflict)
				}
				eligible[order.OrderID] = r
			}
		}
	}
	type staged struct {
		command LedgerCommand
		at      time.Time
	}
	stages := []staged{}
	expected := map[string]staged{}
	seen := map[string]bool{}
	for _, entry := range book.Entries {
		if err := ctx.Err(); err != nil {
			return fail(err)
		}
		for _, out := range entry.Outcomes {
			r, ok := eligible[out.OrderID]
			if !ok || seen[out.OrderID] || (out.Outcome.Status != "cancelled" && out.Outcome.Status != "expired") {
				continue
			}
			seen[out.OrderID] = true
			// Pin the first terminal entry, not the mutable full book head. Appending
			// unrelated observations cannot relabel or repeat this release.
			binding := workerplan.Hash(struct{ Method, BookRootSHA256, TerminalEntrySHA256, OrderID string }{UnfilledEntryReleaseMethod, root.SHA256, entry.SHA256, out.OrderID})
			id := "unfilled-release-" + binding
			effect := Effect{ID: id, SHA256: binding, DecisionID: out.OrderID, Kind: "release", AmountCents: r.Proposal.NotionalCents}
			stage := staged{LedgerCommand{ID: id, Effect: &effect}, entry.Command.At}
			expected[id] = stage
			stages = append(stages, stage)
		}
	}
	// Reject alternate roots, truncated histories and amount-only prior releases
	// for every order in this book, including orders no longer eligible.
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
