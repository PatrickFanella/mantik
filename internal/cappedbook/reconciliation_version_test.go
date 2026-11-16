package cappedbook

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/account"
	"git.subcult.tv/subculture-collective/mantik/internal/alpaca"
)

type interleavedFillsBroker struct {
	*fakePaper
	beforeFills func()
}

func (b interleavedFillsBroker) Fills(ctx context.Context, at time.Time) ([]json.RawMessage, error) {
	b.beforeFills()
	return b.fakePaper.Fills(ctx, at)
}

func TestSyncStateDoesNotAbsorbConcurrentReservation(t *testing.T) {
	for _, withFill := range []bool{false, true} {
		name := "no-write-reconciliation"
		if withFill {
			name = "fill-append-conflict"
		}
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			client, broker, at := paperFixture(t)
			marks := map[string]string{"SPY": "100"}
			first := account.BookOrder{ID: "owned-before-sync", Symbol: "SPY", Quantity: "1", Limit: "100", Side: "buy", Fee: "0", At: at}
			if _, err := client.submitLedgerTest(ctx, first, marks); err != nil {
				t.Fatal(err)
			}
			if withFill {
				broker.fills = []json.RawMessage{rawFill("first-fill", first.ID, "1", at.Add(time.Second))}
			}
			injected := false
			outer := client
			outer.Broker = interleavedFillsBroker{broker, func() {
				if injected {
					return
				}
				injected = true
				second := first
				second.ID = "allocated-during-fills"
				second.At = at.Add(2 * time.Second)
				if _, err := client.submitLedgerTest(ctx, second, marks); err != nil {
					t.Fatal(err)
				}
			}}
			if _, err := outer.SyncState(ctx, at.Add(3*time.Second), marks); !errors.Is(err, account.ErrBookConflict) {
				t.Fatal("concurrent reservation received reconciliation authority", err)
			}
			current, err := client.Store.CappedBook(ctx, client.BookID)
			if err != nil {
				t.Fatal(err)
			}
			for _, event := range current.Events {
				if event.Kind == "halt" || event.Kind == "fill" {
					t.Fatal("ordinary version conflict changed economic state", event)
				}
			}
			// A complete retry discovers the intervening owned order and may then
			// return only its precise reconciled journal prefix, without another POST.
			reconciled, err := outer.SyncState(ctx, at.Add(4*time.Second), marks)
			if err != nil {
				t.Fatal(err)
			}
			current, err = client.Store.CappedBook(ctx, client.BookID)
			if err != nil {
				t.Fatal(err)
			}
			if reconciled.Version != current.Version || broker.posts != 2 {
				t.Fatal(reconciled.Version, current.Version, broker.posts)
			}
		})
	}
}

type interleavedOrderBroker struct {
	*fakePaper
	beforeOrder func()
}

func (b interleavedOrderBroker) Order(ctx context.Context, id string) (alpaca.PaperOrder, error) {
	b.beforeOrder()
	return b.fakePaper.Order(ctx, id)
}

func TestSyncStateIdempotentBindCannotPromoteLaterVersion(t *testing.T) {
	ctx := context.Background()
	client, broker, at := paperFixture(t)
	marks := map[string]string{"SPY": "100"}
	order := account.BookOrder{ID: "unknown-submit", Symbol: "SPY", Quantity: "1", Limit: "100", Side: "buy", Fee: "0", At: at}
	broker.ambiguous = true
	if _, err := client.submitLedgerTest(ctx, order, marks); !errors.Is(err, alpaca.ErrSubmissionUnknown) {
		t.Fatal(err)
	}
	broker.ambiguous = false
	injected := false
	outer := client
	outer.Broker = interleavedOrderBroker{broker, func() {
		if injected {
			return
		}
		injected = true
		// Another reconciler commits the same binding, then another allocation is
		// reserved while the outer order GET is still running. The ordinary store
		// accepts an idempotent bind with the old expected version by design.
		if _, err := client.SyncState(ctx, at, marks); err != nil {
			t.Fatal(err)
		}
		second := order
		second.ID = "concurrent-allocation"
		if _, err := client.submitLedgerTest(ctx, second, marks); err != nil {
			t.Fatal(err)
		}
	}}
	if _, err := outer.SyncState(ctx, at, marks); !errors.Is(err, account.ErrBookConflict) {
		t.Fatal("idempotent bind absorbed a later allocation", err)
	}
	if broker.posts != 2 {
		t.Fatal("reconciliation submitted work", broker.posts)
	}
}
