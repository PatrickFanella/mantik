package storage

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/allocation"
	"git.subcult.tv/subculture-collective/mantik/internal/paperfill"
	"git.subcult.tv/subculture-collective/mantik/internal/workerplan"
)

// fixtureExecutionQuotes returns one usable quote a second after each book's
// placement, past the model's 100 ms latency, so every entry order fills.
func fixtureExecutionQuotes(ask int64) PaperExecutionQuoteSource {
	return func(_ context.Context, _ FeedEvidenceSnapshot, q PaperExecutionQuoteQuery) ([]paperfill.BookCommand, error) {
		at := q.After.Add(time.Second)
		if at.After(q.Through) {
			return nil, nil
		}
		quote := paperfill.Quote{ID: "quote-fixture", SourceSHA256: strings.Repeat("a", 64), Symbol: q.Symbol, Currency: "USD", Quality: "usable", EvidenceClass: q.EvidenceClass, OccurredAt: at, ReceivedAt: at, BidCents: ask, AskCents: ask, BidQuantity: 100000, AskQuantity: 100000}
		return []paperfill.BookCommand{{ID: quote.ID, At: at, Quote: &quote}}, nil
	}
}

func (f eligibilityFixture) step(t *testing.T, journal ownedAllocationJournal, c OwnedExecutionCommand, at time.Time, assessments ...PaperEvaluationAssessment) (ownedAllocationJournal, OwnedExecutionReceipt, error) {
	t.Helper()
	policies, decisions, strategies, err := f.m.ownedAllocationSourcesLocked(f.decide)
	if err != nil {
		t.Fatal(err)
	}
	declarations, err := paperEvaluationDeclarationCatalog(f.m.receiverID, f.m.paperEvaluationPlans, strategies, policies)
	if err != nil {
		t.Fatal(err)
	}
	return appendOwnedExecution(context.Background(), f.m.receiverID, journal, policies, decisions, strategies, f.m.paperDecisions, f.exec, c, func() time.Time { return at }, time.Time{}, declarations, PaperEvaluationAssessmentCatalog{ReceiverID: f.m.receiverID, Records: assessments})
}

func TestOwnedEntryExecutionStepAndReplayGuards(t *testing.T) {
	f := newEligibilityFixture(t)
	f.exec = &ownedExecution{feed: f.m.decisionFeedLocked(), quotes: fixtureExecutionQuotes(1)}
	empty := ownedAllocationJournal{Records: []retainedOwnedAllocation{}}
	journal, receipt, err := f.append(t, empty, f.command, f.at, f.complete)
	if err != nil {
		t.Fatal(err)
	}
	if receipt.Record.Schema != OwnedExecutionSchema || receipt.Record.Entry.Plan.Decisions[0].Status != "reserve_plan" || len(receipt.Record.Executions) != 0 {
		t.Fatal("v16 reservation", receipt.Record.Schema, receipt.Record.Executions)
	}
	// Nothing is due at the reserve clock itself.
	command := OwnedExecutionCommand{ID: "step", ReceiverID: f.m.receiverID}
	same, idle, err := f.step(t, journal, command, f.at, f.complete)
	if err != nil || idle.Record != nil || idle.Status != "no_effects" || len(same.Records) != len(journal.Records) {
		t.Fatal("idle step", idle, err)
	}
	stepped, executed, err := f.step(t, journal, command, f.at.Add(2*time.Second), f.complete)
	if err != nil || executed.Record == nil || len(executed.Record.Executions) != 1 {
		t.Fatal("execution step", executed, err)
	}
	fill := executed.Record.Executions[0].Entries[0]
	if fill.Command.Effect == nil || fill.Command.Effect.Kind != "fill" || fill.Command.Effect.DecisionID != f.command.Decisions[0].ID || !fill.At.Equal(f.at.Add(time.Second)) {
		t.Fatal("entry fill", fill)
	}
	catalog, err := f.replay(t, stepped, f.complete)
	if err != nil || len(catalog.Ledgers) != 1 || catalog.Ledgers[0].Schema != allocation.CostLedgerSchema || len(catalog.Executions) != 1 || catalog.Capital[0].Schema != "mantik-owned-allocation-capital-v3" || catalog.Capital[0].Reservations[0].FilledCents == 0 {
		t.Fatal("replayed execution", err, catalog.Capital)
	}
	if _, retry, err := f.step(t, stepped, command, f.at.Add(time.Hour), f.complete); err != nil || !retry.Duplicate || retry.Record.SHA256 != executed.Record.SHA256 {
		t.Fatal("step retry", retry, err)
	}

	// Replay rederives effects from owner quotes: another quote path, a
	// rehashed amount or a missing quote source all fail closed.
	f.exec.quotes = fixtureExecutionQuotes(2)
	if _, err := f.replay(t, stepped, f.complete); err == nil {
		t.Fatal("replay accepted effects from another quote path")
	}
	f.exec.quotes = fixtureExecutionQuotes(1)
	tampered := clone(stepped)
	e := tampered.Records[len(tampered.Records)-1].Execution
	e.Executions[0].Entries[0].Command.Effect.AmountCents--
	e.SHA256 = ""
	e.SHA256 = workerplan.Hash(*e)
	if _, err := f.replay(t, tampered, f.complete); err == nil {
		t.Fatal("replay accepted a rehashed fill amount")
	}
	exec := f.exec
	f.exec = nil
	if _, err := f.replay(t, stepped, f.complete); !errors.Is(err, ErrOwnedExecutionUnavailable) {
		t.Fatal("replay without execution", err)
	}
	// An older writer cannot add a comparison while a book is open.
	next := f.command
	next.ID = "older-writer"
	if _, _, err := f.append(t, journal, next, f.at.Add(time.Nanosecond), f.complete); !errors.Is(err, ErrOwnedExecutionUnavailable) {
		t.Fatal("v15 append over an open book", err)
	}
	f.exec = exec
}

// Two books reserved at different clocks share one quote: the earlier takes
// its modeled share first and the later sees only what remains.
func TestOwnedEntryBooksShareOccurrenceLiquidity(t *testing.T) {
	t0 := time.Date(2026, 11, 9, 15, 0, 0, 0, time.UTC)
	model := paperfill.Model{Method: paperfill.Method, LiquidityBPS: 5000, LatencyMillis: 100, MaxQuoteAgeSeconds: 60}
	query := PaperExecutionQuoteQuery{RegistrationID: "spy", Symbol: "SPY", Feed: "iex", EvidenceClass: "synthetic_unqualified", MaxQuoteAgeSeconds: 60}
	book := func(id string, placed time.Time) ownedEntryBook {
		root, err := paperfill.NewOccurrenceBook(model, []paperfill.Order{{ID: id, Symbol: "SPY", Currency: "USD", Side: "buy", Kind: "limit", Quantity: 6, LimitCents: 10000, MaxDebitCents: 100000, PlacedAt: placed, ExpiresAt: t0.Add(time.Minute), Mode: "ongoing_paper"}})
		if err != nil {
			t.Fatal(err)
		}
		return ownedEntryBook{policyID: "policy", query: query, root: root, expires: []time.Time{t0.Add(time.Minute)}}
	}
	at := t0.Add(2 * time.Second)
	quote := paperfill.Quote{ID: "quote-shared", SourceSHA256: strings.Repeat("a", 64), Symbol: "SPY", Currency: "USD", Quality: "usable", EvidenceClass: query.EvidenceClass, OccurredAt: at, ReceivedAt: at, BidCents: 99, AskCents: 100, BidQuantity: 20, AskQuantity: 20}
	x := ownedExecution{quotes: func(_ context.Context, _ FeedEvidenceSnapshot, q PaperExecutionQuoteQuery) ([]paperfill.BookCommand, error) {
		if !at.After(q.After) || at.After(q.Through) {
			return nil, nil
		}
		return []paperfill.BookCommand{{ID: quote.ID, At: at, Quote: &quote}}, nil
	}}
	books := []ownedEntryBook{book("first", t0), book("second", t0.Add(time.Second))}
	advanced, err := advancedBooks(context.Background(), books, x, t0, t0.Add(3*time.Second))
	if err != nil || len(advanced) != 2 {
		t.Fatal(len(advanced), err)
	}
	filled := func(b paperfill.Book) int64 {
		p, err := paperfill.ReplayBook(context.Background(), b)
		if err != nil {
			t.Fatal(err)
		}
		return p.Orders[0].Projection.FilledQuantity
	}
	// Capacity is 20*5000/10000 = 10 shares: 6 to the first book, 4 left.
	if filled(advanced[0]) != 6 || filled(advanced[1]) != 4 {
		t.Fatal("shared fills", filled(advanced[0]), filled(advanced[1]))
	}
	// A settled earlier book still counts when a later open book overlaps it.
	books[0].expires = []time.Time{t0.Add(2500 * time.Millisecond)}
	books[0].root.Orders[0].ExpiresAt = books[0].expires[0]
	if books[0].root, err = paperfill.NewOccurrenceBook(model, books[0].root.Orders); err != nil {
		t.Fatal(err)
	}
	advanced, err = advancedBooks(context.Background(), books, x, t0.Add(2500*time.Millisecond), t0.Add(3*time.Second))
	if err != nil || len(advanced) != 1 || filled(advanced[1]) != 4 {
		t.Fatal("settled book liquidity", len(advanced), err)
	}
}

func TestSharedDisplayRestoresRemainingCapacity(t *testing.T) {
	for _, bps := range []int64{1, 3333, 5000, 7777, 10000} {
		for displayed := int64(0); displayed <= 300; displayed++ {
			capacity := displayed * bps / 10000
			for used := int64(0); used <= capacity+1; used++ {
				d := sharedDisplay(displayed, used, bps)
				if d > displayed || d*bps/10000 != max(0, capacity-used) {
					t.Fatal(bps, displayed, used, d)
				}
			}
		}
	}
}
