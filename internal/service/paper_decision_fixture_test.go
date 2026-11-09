package service

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/alpaca"
	"git.subcult.tv/subculture-collective/mantik/internal/feedcollector"
	"git.subcult.tv/subculture-collective/mantik/internal/paperstrategy"
	"git.subcult.tv/subculture-collective/mantik/internal/storage"
)

// quoteFixture is a claimed registration with a calendar and a registered
// strategy, ready to publish quotes through the real validation path.
type quoteFixture struct {
	store       *storage.Memory
	owner       string
	reg         feedcollector.Registration
	fence       feedcollector.Fence
	at          time.Time
	definition  paperstrategy.Definition
	strategy    paperstrategy.Receipt
	date        string
	calendarRaw string
	t           *testing.T
}

func (f *quoteFixture) now() time.Time { return f.at }

func (f *quoteFixture) publish(r alpaca.Recording) {
	f.t.Helper()
	out, err := alpaca.Normalize(r)
	if err != nil {
		f.t.Fatal(err)
	}
	for _, input := range out.Inputs {
		if _, err := f.store.SubmitCollectorResearch(f.t.Context(), f.owner, f.fence, input, f.now); err != nil {
			f.t.Fatal(err)
		}
	}
	raw, _ := json.Marshal(r)
	if _, err := f.store.PublishFeedEvidence(f.t.Context(), f.owner, f.reg.ID, recordingKey(r), f.fence, raw, f.now, publicationCheck(f.reg.ID, f.fence, r)); err != nil {
		f.t.Fatal(err)
	}
}

func (f *quoteFixture) recording(id, capability, feed, raw, start string) alpaca.Recording {
	r := alpaca.Recording{Schema: alpaca.RecordingSchema, Currency: "USD", Symbols: []string{"SPY"}, Revision: 1, EvidenceClass: f.reg.EvidenceClass, AccessRef: f.reg.AccessRef, MaxQuoteAgeSeconds: 60}
	r.CaptureID, r.Capability, r.Feed, r.ReceivedAt, r.Original, r.SHA256 = id, capability, feed, f.at, raw, alpaca.Digest(raw)
	r.RawRef = fmt.Sprintf("journal:%s:capture-%s-r1.json", f.reg.JournalID, id)
	r.Start, r.End = start, start
	return r
}

// quote publishes a fresh quote at the fixture clock, the bid and ask both at
// cents.
func (f *quoteFixture) quote(id string, cents int) {
	f.t.Helper()
	stamp := f.at.Add(-time.Second).Format(time.RFC3339Nano)
	f.publish(f.recording(id, "latest_quotes", "iex", fmt.Sprintf(`{"quotes":{"SPY":{"t":%q,"ap":%d.%02d,"bp":%d.%02d,"as":10,"bs":10}}}`, stamp, cents/100, cents%100, cents/100, cents%100), ""))
}

func newQuoteFixture(t *testing.T) *quoteFixture {
	t.Helper()
	store := storage.NewMemory()
	snapshot, err := store.FeedCollectors(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	zone, _ := time.LoadLocation("America/New_York")
	f := &quoteFixture{store: store, owner: snapshot.ReceiverID, at: time.Date(2026, 11, 9, 10, 0, 0, 0, zone).UTC(), t: t}
	f.reg = feedcollector.Registration{ID: "window-feed", Provider: "alpaca", Feed: "iex", Symbol: "SPY", EvidenceClass: "prospective_market_unqualified", JournalID: "window-journal", AccessRef: "window-access", LeaseSeconds: 300}
	for _, c := range []feedcollector.Command{{RequestID: "register", Operation: "register", RegistrationID: f.reg.ID, Registration: &f.reg}, {RequestID: "claim", Operation: "claim", RegistrationID: f.reg.ID, OwnerID: "worker"}} {
		if _, err := store.FeedCollectorCommand(t.Context(), f.owner, c, f.now); err != nil {
			t.Fatal(err)
		}
	}
	f.fence = feedcollector.Fence{RegistrationID: f.reg.ID, OwnerID: "worker", Generation: 1}
	f.date = f.at.In(zone).Format("2006-01-02")
	f.calendarRaw = fmt.Sprintf(`[{"date":%q,"open":"09:30","close":"16:00"}]`, f.date)
	f.publish(f.recording("calendar", "market_calendar", "regular_session", f.calendarRaw, f.date))
	f.definition = windowStrategy(f.reg)
	f.strategy, err = store.RegisterPaperStrategy(t.Context(), paperstrategy.Command{ReceiverID: f.owner, Definition: f.definition}, f.now)
	if err != nil {
		t.Fatal(err)
	}
	return f
}

// declaredStore makes the fixture strategy eligible for the decision worker
// without building a whole evaluation plan.
type declaredStore struct {
	*storage.Memory
	catalog storage.PaperEvaluationDeclarationCatalog
}

func (s declaredStore) PaperEvaluationDeclarations(ctx context.Context) (storage.PaperEvaluationDeclarationCatalog, error) {
	return s.catalog, ctx.Err()
}

func (f *quoteFixture) declared() declaredStore {
	var d storage.PaperEvaluationDeclaration
	d.Command.Plan.Variants = []storage.PaperEvaluationVariant{{ID: f.definition.ID, SHA256: f.strategy.Record.SHA256}}
	return declaredStore{f.store, storage.PaperEvaluationDeclarationCatalog{ReceiverID: f.owner, Records: []storage.PaperEvaluationDeclaration{d}}}
}
