package storage

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

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
	if err != nil || len(catalog.Executions) != 1 || catalog.Capital[0].Schema != "mantik-owned-allocation-capital-v3" || catalog.Capital[0].Reservations[0].FilledCents == 0 {
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
