package paperfill

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand/v2"
	"reflect"
	"testing"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/workerplan"
)

// applyBookByReplay is ApplyBook before BookAppender: it replays the whole
// book for every command. It is the oracle for the appender.
func applyBookByReplay(ctx context.Context, b Book, c BookCommand) (Book, BookProjection, BookReceipt, error) {
	p, err := ReplayBook(ctx, b)
	if err != nil {
		return Book{}, BookProjection{}, BookReceipt{}, err
	}
	c = normalizeBookCommand(c)
	for i, e := range b.Entries {
		if e.Command.ID == c.ID {
			if !reflect.DeepEqual(e.Command, c) {
				return Book{}, BookProjection{}, BookReceipt{}, ErrConflict
			}
			return detach(b), p, BookReceipt{Sequence: i + 1, EntrySHA256: e.SHA256, Duplicate: true, Outcomes: detach(e.Outcomes)}, nil
		}
	}
	if len(b.Entries) >= MaxEntries {
		return Book{}, BookProjection{}, BookReceipt{}, ErrCapacity
	}
	seen := map[string]string{}
	for _, e := range b.Entries {
		if e.Command.Quote != nil {
			seen[e.Command.Quote.ID] = workerplan.Hash(*e.Command.Quote)
		}
	}
	next, out, err := bookStep(b, p, c, seen)
	if err != nil {
		return Book{}, BookProjection{}, BookReceipt{}, err
	}
	previous := b.SHA256
	if len(b.Entries) > 0 {
		previous = b.Entries[len(b.Entries)-1].SHA256
	}
	e := BookEntry{Command: c, Outcomes: out, PreviousSHA256: previous}
	e.SHA256 = workerplan.Hash(e)
	updated := detach(b)
	updated.Entries = append(updated.Entries, e)
	updated.SHA256 = ""
	updated.SHA256 = workerplan.Hash(updated)
	raw, err := json.Marshal(updated)
	if err != nil || len(raw) > MaxBookBytes {
		return Book{}, BookProjection{}, BookReceipt{}, ErrCapacity
	}
	return updated, detach(next), BookReceipt{Sequence: len(updated.Entries), EntrySHA256: e.SHA256, Outcomes: detach(out)}, nil
}

// One appender must reach exactly the books, projections and receipts that
// replaying the whole book for each command reaches, including duplicates.
func TestBookAppenderMatchesRepeatedApply(t *testing.T) {
	random := rand.New(rand.NewPCG(5, 8))
	for _, occurrence := range []bool{false, true} {
		for round := range 8 {
			m := model()
			m.LiquidityBPS = 1 + random.Int64N(10000)
			m.FeeBPS = random.Int64N(100)
			orders := bookOrders()
			for i := range orders {
				orders[i].Quantity = 1 + random.Int64N(30)
			}
			b := mustBook(t, m, orders)
			if occurrence {
				var err error
				if b, err = NewOccurrenceBook(m, orders); err != nil {
					t.Fatal(err)
				}
			}
			a, err := NewBookAppender(context.Background(), b)
			if err != nil {
				t.Fatal(err)
			}
			commands, cancelled := []BookCommand{}, false
			var current BookProjection
			for i := range 32 {
				q := quote(fmt.Sprintf("q-%d-%d", round, i), 2+i)
				q.BidQuantity, q.AskQuantity = random.Int64N(20), random.Int64N(20)
				if random.IntN(4) == 0 && i > 0 {
					q.OccurredAt = commands[len(commands)-1].At
				}
				step := func(c BookCommand) {
					commands = append(commands, c)
					want, wantProjection, wantReceipt, wantErr := applyBookByReplay(context.Background(), b, c)
					projection, receipt, err := a.Apply(context.Background(), c)
					if err != nil || wantErr != nil {
						t.Fatal(c.ID, err, wantErr)
					}
					got := a.Book()
					if !reflect.DeepEqual(got, want) || !reflect.DeepEqual(projection, wantProjection) || !reflect.DeepEqual(receipt, wantReceipt) {
						t.Fatal("appender diverged at", c.ID)
					}
					raw, _ := json.Marshal(got)
					if a.size != len(raw) {
						t.Fatal("tracked size", a.size, len(raw))
					}
					b, current = want, projection
				}
				step(BookCommand{ID: q.ID, At: q.ReceivedAt, Quote: &q})
				if i == 10 {
					step(commands[3])
				}
				if i >= 20 && !cancelled && current.Orders[1].Projection.Status == "working" {
					step(BookCommand{ID: "cancel-b", At: q.ReceivedAt, CancelOrderID: "b-buy"})
					cancelled = true
				}
			}
			if len(b.Entries) != 32+boolCount(cancelled) {
				t.Fatal("entries", len(b.Entries))
			}
		}
	}
}

func boolCount(v bool) int {
	if v {
		return 1
	}
	return 0
}

func TestBookAppenderStopsAfterError(t *testing.T) {
	b := mustBook(t, model(), bookOrders())
	a, err := NewBookAppender(context.Background(), b)
	if err != nil {
		t.Fatal(err)
	}
	q := quote("first", 2)
	if _, _, err := a.Apply(context.Background(), BookCommand{ID: "first", At: q.ReceivedAt, Quote: &q}); err != nil {
		t.Fatal(err)
	}
	before := a.Book()
	late := quote("late", 1)
	if _, _, err := a.Apply(context.Background(), BookCommand{ID: "late", At: late.ReceivedAt, Quote: &late}); !errors.Is(err, ErrInvalid) {
		t.Fatal("clock regression", err)
	}
	next := quote("next", 3)
	if _, _, err := a.Apply(context.Background(), BookCommand{ID: "next", At: next.ReceivedAt.Add(time.Second), Quote: &next}); !errors.Is(err, ErrInvalid) {
		t.Fatal("appender continued after an error", err)
	}
	if !reflect.DeepEqual(a.Book(), before) {
		t.Fatal("failed command changed the book")
	}
}
