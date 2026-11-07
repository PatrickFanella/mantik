package service

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/alpaca"
	"git.subcult.tv/subculture-collective/mantik/internal/domain"
	"git.subcult.tv/subculture-collective/mantik/internal/feedcollector"
	"git.subcult.tv/subculture-collective/mantik/internal/storage"
	"git.subcult.tv/subculture-collective/mantik/internal/workerplan"
)

// withOutcomeCalendar admits a regular-session calendar one second after the
// decision and, when published, retains its capture at the same clock.
func withOutcomeCalendar(t *testing.T, published bool) storage.PaperOutcomeSources {
	t.Helper()
	s := outcomeMarketFixture(t)
	at := s.Basis.Decision.FinalizedAt.Add(time.Second)
	raw := `[{"date":"2026-10-21","open":"09:30","close":"16:00"}]`
	rec := alpaca.Recording{Currency: "USD", Schema: alpaca.RecordingSchema, CaptureID: "calendar", Capability: "market_calendar", Feed: "regular_session", Symbols: []string{"SPY"}, Start: "2026-10-21", End: "2026-10-21", Revision: 1, ReceivedAt: at, EvidenceClass: "synthetic_unqualified", AccessRef: "fixture", RawRef: "journal:journal-one:capture-calendar-r1.json", Original: raw, SHA256: alpaca.Digest(raw), MaxQuoteAgeSeconds: 60}
	n, err := alpaca.Normalize(rec)
	if err != nil {
		t.Fatal(err)
	}
	feed := &s.Basis.Observation.Feed
	fence := feedcollector.Fence{RegistrationID: "spy", OwnerID: "owner", Generation: 1}
	for _, input := range n.Inputs {
		feed.Collectors.Admissions[input.ID] = feedcollector.Admission{Fence: fence, InputSHA256: feedcollector.InputDigest(input), At: at}
		feed.Routing.Inputs[input.ID] = domain.RetainedRoutingInput{Input: input}
	}
	if published {
		payload, _ := json.Marshal(rec)
		feed.Records["spy:calendar-r1"] = storage.FeedEvidenceRecord{RegistrationID: "spy", Key: "calendar-r1", Fence: fence, PublishedAt: at, Recording: payload}
	}
	s.Outcome.BasisSHA256 = workerplan.Hash(s.Basis)
	s.Outcome.SHA256 = ""
	s.Outcome.SHA256 = workerplan.Hash(s.Outcome)
	return s
}

// A memo shared across differing feeds must return exactly the fresh result
// for each, because its key binds every record and admission by the cut.
func TestSessionMemoMatchesFreshInspectionAcrossFeeds(t *testing.T) {
	variants := []storage.PaperOutcomeSources{outcomeMarketFixture(t), withOutcomeCalendar(t, true), withOutcomeCalendar(t, false)}
	fresh := make([]PaperOutcomeMarketEvidence, len(variants))
	for i, v := range variants {
		out, err := inspectPaperOutcomeMarket(context.Background(), v)
		if err != nil {
			t.Fatal(i, err)
		}
		fresh[i] = out
	}
	if fresh[0].SHA256 == fresh[1].SHA256 || fresh[1].SHA256 == fresh[2].SHA256 || fresh[0].SHA256 == fresh[2].SHA256 {
		t.Fatal("calendar variants did not change session evidence")
	}
	ctx := withSessionMemo(context.Background())
	for range 2 {
		for i, v := range variants {
			out, err := inspectPaperOutcomeMarket(ctx, v)
			if err != nil || !reflect.DeepEqual(out, fresh[i]) {
				t.Fatal("memoized inspection diverged", i, err)
			}
		}
	}
	if len(sessionMemoFrom(ctx).entries) == 0 {
		t.Fatal("memo unused")
	}
}
