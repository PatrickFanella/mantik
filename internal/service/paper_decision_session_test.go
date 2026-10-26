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

// Negative/manufactured inspector fixtures, not owner custody or qualification.
func decisionCalendarFixture(t *testing.T, s *storage.PaperDecisionSources, id, raw string, received, admitted, published time.Time) {
	t.Helper()
	r := alpaca.Recording{Currency: "USD", Schema: alpaca.RecordingSchema, CaptureID: id, Capability: "market_calendar", Feed: "regular_session", Symbols: []string{"SPY"}, Start: "2026-10-21", End: "2026-10-21", Revision: 1, ReceivedAt: received, EvidenceClass: "synthetic_unqualified", AccessRef: "fixture", RawRef: "journal:journal-one:capture-" + id + "-r1.json", Original: raw, SHA256: alpaca.Digest(raw), MaxQuoteAgeSeconds: 60}
	n, err := alpaca.Normalize(r)
	if err != nil {
		t.Fatal(err)
	}
	fence := feedcollector.Fence{RegistrationID: "spy", OwnerID: "owner", Generation: 1}
	for _, i := range n.Inputs {
		s.Basis.Feed.Collectors.Admissions[i.ID] = feedcollector.Admission{Fence: fence, InputSHA256: feedcollector.InputDigest(i), At: admitted}
		s.Basis.Feed.Routing.Inputs[i.ID] = domain.RetainedRoutingInput{Input: i}
	}
	if !published.IsZero() {
		payload, _ := json.Marshal(r)
		s.Basis.Feed.Records["spy:"+id+"-r1"] = storage.FeedEvidenceRecord{RegistrationID: "spy", Key: id + "-r1", Fence: fence, PublishedAt: published, Recording: payload}
	}
	s.Decision.BasisSHA256 = workerplan.Hash(s.Basis)
	s.Decision.SHA256 = ""
	s.Decision.SHA256 = workerplan.Hash(s.Decision)
}

func TestDecisionQuoteSessionFinalizationCut(t *testing.T) {
	for _, tc := range []struct {
		name, raw, code               string
		received, admitted, published time.Duration
		missing                       bool
		eligible                      bool
	}{
		{"available", `[{"date":"2026-10-21","open":"09:30","close":"16:00"}]`, "within_regular_session", -time.Second, 0, 0, false, true},
		{"admitted_after_quote_before_finalization", `[{"date":"2026-10-21","open":"09:30","close":"16:00"}]`, "within_regular_session", -time.Second, time.Second, time.Second, false, true},
		{"exact_finalization", `[{"date":"2026-10-21","open":"09:30","close":"16:00"}]`, "within_regular_session", -time.Second, 2 * time.Second, 2 * time.Second, false, true},
		{"admitted_after_finalization", `[{"date":"2026-10-21","open":"09:30","close":"16:00"}]`, "calendar_capture_missing", -time.Second, 3 * time.Second, 0, false, false},
		{"unpublished", `[{"date":"2026-10-21","open":"09:30","close":"16:00"}]`, "calendar_publication_pending", -time.Second, time.Second, 0, true, false},
		{"holiday", `[]`, "calendar_no_session", -time.Second, 0, 0, false, false},
		{"preopen", `[{"date":"2026-10-21","open":"11:00","close":"16:00"}]`, "before_regular_open", -time.Second, 0, 0, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := marketInspectorFixture(t)
			at := s.Decision.FinalizedAt
			s.Decision.FinalizedAt = at.Add(2 * time.Second)
			published := at.Add(tc.published)
			if tc.missing {
				published = time.Time{}
			}
			decisionCalendarFixture(t, &s, "calendar", tc.raw, at.Add(tc.received), at.Add(tc.admitted), published)
			before := workerplan.Hash(s)
			got, err := inspectPaperDecisionMarket(context.Background(), s)
			if err != nil {
				t.Fatal(err)
			}
			session := got.SessionEvidence
			if got.Schema != "mantik-owned-decision-market-evidence-v3" || session == nil || session.Method != "owned-decision-quote-session-at-finalization-v1" || !session.Evidence.At.Equal(s.Decision.FinalizedAt) || session.Evidence.Session.Code != tc.code || session.Evidence.Quote.RegularSessionEligible != tc.eligible || session.Evidence.Quote.InputID != got.Quote.InputID || got.ExecutionAvailable || got.AdmissionAvailable || got.QualificationCredit || !got.Quote.ShareQuantitiesAvailable {
				t.Fatal(got, session)
			}
			if tc.eligible && (len(session.CalendarSources) != 1 || session.CalendarSources[0].CaptureSHA256 == "" || session.CalendarSources[0].AvailableAt.After(s.Decision.FinalizedAt)) {
				t.Fatal(session)
			}
			again, err := inspectPaperDecisionMarket(context.Background(), s)
			if err != nil || !reflect.DeepEqual(got, again) || before != workerplan.Hash(s) {
				t.Fatal("unstable or mutated source", err)
			}
		})
	}
}
func TestDecisionQuoteSessionCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	got, err := inspectPaperDecisionMarket(ctx, marketInspectorFixture(t))
	if err != context.Canceled || !reflect.DeepEqual(got, PaperDecisionMarketEvidence{}) {
		t.Fatal(got, err)
	}
}

func TestDecisionQuoteSessionPendingSuppressesOlder(t *testing.T) {
	s := marketInspectorFixture(t)
	at := s.Decision.FinalizedAt
	decisionCalendarFixture(t, &s, "old", `[{"date":"2026-10-21","open":"09:30","close":"16:00"}]`, at.Add(-2*time.Second), at.Add(-2*time.Second), at.Add(-2*time.Second))
	decisionCalendarFixture(t, &s, "new", `[{"date":"2026-10-21","open":"11:00","close":"16:00"}]`, at.Add(-time.Second), at, time.Time{})
	got, err := inspectPaperDecisionMarket(context.Background(), s)
	if err != nil || got.SessionEvidence == nil || got.SessionEvidence.Evidence.Quote.RegularSessionEligible || got.SessionEvidence.Evidence.Session.Code != "calendar_publication_pending" || len(got.SessionEvidence.PendingCalendarAdmissions) != 1 {
		t.Fatal(got, err)
	}
}
