package service

import (
	"fmt"
	"reflect"
	"testing"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/domain"
	"git.subcult.tv/subculture-collective/mantik/internal/feedcollector"
	"git.subcult.tv/subculture-collective/mantik/internal/paperstrategy"
	"git.subcult.tv/subculture-collective/mantik/internal/storage"
)

// A windowed basis must evaluate exactly as the full history would. Windowing
// is storage's job; this exercises it through the retained record the store
// actually keeps, against the evaluator, at each point in a growing feed.
func TestWindowedBasisEvaluatesLikeTheFullHistory(t *testing.T) {
	f := newQuoteFixture(t)
	store, reg, strategy, definition := f.store, f.reg, f.strategy, f.definition
	owner, clock, at := f.owner, f.now, &f.at
	var err error
	publish, recording, date, calendarRaw := f.publish, f.recording, f.date, f.calendarRaw
	var last storage.FeedEvidenceSnapshot
	command := storage.PaperDecisionCommand{ReceiverID: owner, StrategyID: definition.ID, StrategySHA256: strategy.Record.SHA256, RegistrationID: reg.ID}
	for n := range 12 {
		*at = at.Add(20 * time.Second)
		if n > 0 && n%3 == 0 {
			publish(recording(fmt.Sprintf("calendar-%02d", n), "market_calendar", "regular_session", calendarRaw, date))
		}
		stamp := at.Add(-time.Second).Format(time.RFC3339Nano)
		cents := 10000 + n
		publish(recording(fmt.Sprintf("quote-%02d", n), "latest_quotes", "iex", fmt.Sprintf(`{"quotes":{"SPY":{"t":%q,"ap":%d.%02d,"bp":%d.%02d,"as":10,"bs":10}}}`, stamp, cents/100, cents%100, cents/100, cents%100), ""))
		last, err = store.FeedEvidence(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		c := command
		c.ID = fmt.Sprintf("window-%02d", n)
		var windowed storage.PaperDecisionBasis
		eval := func(c storage.PaperDecisionCommand, b storage.PaperDecisionBasis, evalAt time.Time) (storage.PaperDecisionEvaluation, error) {
			windowed = b
			return evaluateOwnedPaperDecision(c, b, evalAt)
		}
		receipt, err := store.FinalizePaperDecision(t.Context(), c, clock, eval)
		if err != nil {
			t.Fatal(n, err)
		}
		full, err := evaluateOwnedPaperDecision(c, storage.PaperDecisionBasis{Strategy: strategy.Record, Feed: last}, *at)
		if err != nil {
			t.Fatal(n, err)
		}
		if !reflect.DeepEqual(full, receipt.Record.Evaluation) {
			t.Fatal("window changed the evaluation at", n, full, receipt.Record.Evaluation)
		}
		if receipt.Record.Evaluation.Action != "abstain" && receipt.Record.Evaluation.Action != "entry_candidate" || !full.Coverage.Quote.Usable {
			t.Fatal("fixture quote not evaluated", n, full)
		}
		if windowed.Prefix == nil || len(windowed.Feed.Records) > 5 {
			t.Fatal("window kept history", n, len(windowed.Feed.Records), windowed.Prefix)
		}
	}
}

func windowStrategy(r feedcollector.Registration) paperstrategy.Definition {
	return paperstrategy.Definition{ID: "window-strategy", Method: paperstrategy.Method, Symbol: "SPY", Feed: "iex", InputEvidenceClass: r.EvidenceClass, EntryAtOrBelowCents: 10000, Quantity: 10, Safety: domain.Safety{MaxNotionalCents: 100000, StopCents: 9500, TargetCents: 11000, MaxHoldingSeconds: 3600, MaxQuoteAgeSeconds: 60}}
}
