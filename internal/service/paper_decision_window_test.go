package service

import (
	"encoding/json"
	"fmt"
	"reflect"
	"testing"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/alpaca"
	"git.subcult.tv/subculture-collective/mantik/internal/domain"
	"git.subcult.tv/subculture-collective/mantik/internal/feedcollector"
	"git.subcult.tv/subculture-collective/mantik/internal/paperstrategy"
	"git.subcult.tv/subculture-collective/mantik/internal/storage"
)

// A windowed basis must evaluate exactly as the full history would. Windowing
// is storage's job; this exercises it through the retained record the store
// actually keeps, against the evaluator, at each point in a growing feed.
func TestWindowedBasisEvaluatesLikeTheFullHistory(t *testing.T) {
	store := storage.NewMemory()
	snapshot, err := store.FeedCollectors(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	owner := snapshot.ReceiverID
	zone, _ := time.LoadLocation("America/New_York")
	at := time.Date(2026, 11, 9, 10, 0, 0, 0, zone).UTC()
	clock := func() time.Time { return at }
	reg := feedcollector.Registration{ID: "window-feed", Provider: "alpaca", Feed: "iex", Symbol: "SPY", EvidenceClass: "prospective_market_unqualified", JournalID: "window-journal", AccessRef: "window-access", LeaseSeconds: 300}
	for _, c := range []feedcollector.Command{{RequestID: "register", Operation: "register", RegistrationID: reg.ID, Registration: &reg}, {RequestID: "claim", Operation: "claim", RegistrationID: reg.ID, OwnerID: "worker"}} {
		if _, err := store.FeedCollectorCommand(t.Context(), owner, c, clock); err != nil {
			t.Fatal(err)
		}
	}
	fence := feedcollector.Fence{RegistrationID: reg.ID, OwnerID: "worker", Generation: 1}
	publish := func(r alpaca.Recording) {
		t.Helper()
		out, err := alpaca.Normalize(r)
		if err != nil {
			t.Fatal(err)
		}
		for _, input := range out.Inputs {
			if _, err := store.SubmitCollectorResearch(t.Context(), owner, fence, input, clock); err != nil {
				t.Fatal(err)
			}
		}
		raw, _ := json.Marshal(r)
		if _, err := store.PublishFeedEvidence(t.Context(), owner, reg.ID, recordingKey(r), fence, raw, clock, publicationCheck(reg.ID, fence, r)); err != nil {
			t.Fatal(err)
		}
	}
	base := alpaca.Recording{Schema: alpaca.RecordingSchema, Currency: "USD", Symbols: []string{"SPY"}, Revision: 1, EvidenceClass: reg.EvidenceClass, AccessRef: reg.AccessRef, MaxQuoteAgeSeconds: 60}
	recording := func(id, capability, feed, raw string, start string) alpaca.Recording {
		r := base
		r.CaptureID, r.Capability, r.Feed, r.ReceivedAt, r.Original, r.SHA256 = id, capability, feed, at, raw, alpaca.Digest(raw)
		r.RawRef = fmt.Sprintf("journal:%s:capture-%s-r1.json", reg.JournalID, id)
		r.Start, r.End = start, start
		return r
	}
	date := at.In(zone).Format("2006-01-02")
	calendarRaw := fmt.Sprintf(`[{"date":%q,"open":"09:30","close":"16:00"}]`, date)
	publish(recording("calendar", "market_calendar", "regular_session", calendarRaw, date))
	var last storage.FeedEvidenceSnapshot
	definition := windowStrategy(reg)
	strategy, err := store.RegisterPaperStrategy(t.Context(), paperstrategy.Command{ReceiverID: owner, Definition: definition}, clock)
	if err != nil {
		t.Fatal(err)
	}
	command := storage.PaperDecisionCommand{ReceiverID: owner, StrategyID: definition.ID, StrategySHA256: strategy.Record.SHA256, RegistrationID: reg.ID}
	for n := range 12 {
		at = at.Add(20 * time.Second)
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
		eval := func(c storage.PaperDecisionCommand, b storage.PaperDecisionBasis, at time.Time) (storage.PaperDecisionEvaluation, error) {
			windowed = b
			return evaluateOwnedPaperDecision(c, b, at)
		}
		receipt, err := store.FinalizePaperDecision(t.Context(), c, clock, eval)
		if err != nil {
			t.Fatal(n, err)
		}
		full, err := evaluateOwnedPaperDecision(c, storage.PaperDecisionBasis{Strategy: strategy.Record, Feed: last}, at)
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
