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

func outcomeCalendarFixture(t *testing.T, s *storage.PaperOutcomeSources, id, raw string, received, admitted, published time.Time) {
	t.Helper()
	r := alpaca.Recording{Currency: "USD", Schema: alpaca.RecordingSchema, CaptureID: id, Capability: "market_calendar", Feed: "regular_session", Symbols: []string{"SPY"}, Start: "2026-10-21", End: "2026-10-21", Revision: 1, ReceivedAt: received, EvidenceClass: "synthetic_unqualified", AccessRef: "fixture", RawRef: "journal:journal-one:capture-" + id + "-r1.json", Original: raw, SHA256: alpaca.Digest(raw), MaxQuoteAgeSeconds: 60}
	n, err := alpaca.Normalize(r)
	if err != nil {
		t.Fatal(err)
	}
	fence := feedcollector.Fence{RegistrationID: "spy", OwnerID: "owner", Generation: 1}
	for _, i := range n.Inputs {
		s.Basis.Observation.Feed.Collectors.Admissions[i.ID] = feedcollector.Admission{Fence: fence, InputSHA256: feedcollector.InputDigest(i), At: admitted}
		s.Basis.Observation.Feed.Routing.Inputs[i.ID] = domain.RetainedRoutingInput{Input: i}
	}
	if !published.IsZero() {
		payload, _ := json.Marshal(r)
		s.Basis.Observation.Feed.Records["spy:"+id+"-r1"] = storage.FeedEvidenceRecord{RegistrationID: "spy", Key: id + "-r1", Fence: fence, PublishedAt: published, Recording: payload}
	}
	s.Outcome.BasisSHA256 = workerplan.Hash(s.Basis)
	s.Outcome.SHA256 = ""
	s.Outcome.SHA256 = workerplan.Hash(s.Outcome)
}
func TestOutcomeQuoteSessionAvailabilityCut(t *testing.T) {
	const open = `[{"date":"2026-10-21","open":"09:30","close":"16:00"}]`
	const closed = `[{"date":"2026-10-21","open":"09:30","close":"10:00"}]`
	for _, tc := range []struct {
		name, raw                     string
		received, admitted, published time.Duration
		status, code                  string
		eligible                      bool
	}{
		{"stale_calendar", open, -86401 * time.Second, -86401 * time.Second, -86401 * time.Second, "unknown", "stale_calendar_capture", false},
		{"available", open, -time.Second, -time.Second, -time.Second, "open", "within_regular_session", true},
		{"published_late", open, -time.Second, -time.Second, 3 * time.Second, "unknown", "calendar_publication_pending", false},
		{"admitted_late", open, -time.Second, 3 * time.Second, -time.Second, "unknown", "calendar_capture_missing", false},
		{"not_published", open, -time.Second, -time.Second, 0, "unknown", "calendar_publication_pending", false},
		{"exact_publication", open, -time.Second, -time.Second, 2 * time.Second, "open", "within_regular_session", true},
		{"exact_admission", open, -time.Second, 2 * time.Second, -time.Second, "open", "within_regular_session", true},
		{"early_close", closed, -time.Second, -time.Second, -time.Second, "closed", "after_regular_close", false},
		{"holiday", "[]", -time.Second, -time.Second, -time.Second, "closed", "calendar_no_session", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := outcomeMarketFixture(t)
			at := s.Basis.Decision.FinalizedAt
			published := at.Add(tc.published)
			if tc.name == "not_published" {
				published = time.Time{}
			}
			outcomeCalendarFixture(t, &s, "calendar", tc.raw, at.Add(tc.received), at.Add(tc.admitted), published)
			before := workerplan.Hash(s)
			out, err := inspectPaperOutcomeMarket(context.Background(), s)
			if err != nil {
				t.Fatal(err)
			}
			var row PaperOutcomeMarketRow
			for _, r := range out.Rows {
				if r.CaptureKey == "spy:during-r1" {
					row = r
				}
			}
			session := row.SessionEvidence
			if session == nil || session.Evidence.Session.Status != tc.status || session.Evidence.Session.Code != tc.code || session.Evidence.Quote.RegularSessionEligible != tc.eligible || !session.Evidence.At.Equal(row.AvailableAt) || session.Evidence.QualificationCredit || session.Evidence.CollectorLivenessVerified || session.Evidence.GlobalReadinessConfigured {
				t.Fatal(row, session)
			}
			if tc.code == "calendar_publication_pending" && (len(session.PendingCalendarAdmissions) != 1 || session.PendingCalendarAdmissions[0].InputSHA256 == "" || session.PendingCalendarAdmissions[0].At.After(row.AvailableAt)) {
				t.Fatal("missing pending calendar pins", session)
			}
			if tc.eligible && (len(session.CalendarSources) != 1 || session.CalendarSources[0].CaptureSHA256 == "" || len(session.CalendarSources[0].Admissions) != 1 || session.CalendarSources[0].AvailableAt.After(row.AvailableAt)) {
				t.Fatal("missing actual calendar pins", session)
			}
			if workerplan.Hash(s) != before {
				t.Fatal("inspector mutated owner source")
			}
			again, err := inspectPaperOutcomeMarket(context.Background(), s)
			if err != nil || !reflect.DeepEqual(out, again) {
				t.Fatal("unstable session evidence", err)
			}
		})
	}
}
func TestOutcomeQuoteSessionPendingCalendarSuppressesOlder(t *testing.T) {
	s := outcomeMarketFixture(t)
	at := s.Basis.Decision.FinalizedAt
	outcomeCalendarFixture(t, &s, "old", `[{"date":"2026-10-21","open":"09:30","close":"16:00"}]`, at.Add(-2*time.Second), at.Add(-2*time.Second), at.Add(-2*time.Second))
	outcomeCalendarFixture(t, &s, "new", `[{"date":"2026-10-21","open":"09:30","close":"10:00"}]`, at.Add(-time.Second), at.Add(time.Second), time.Time{})
	out, err := inspectPaperOutcomeMarket(context.Background(), s)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range out.Rows {
		if r.CaptureKey == "spy:during-r1" && (r.SessionEvidence == nil || r.SessionEvidence.Evidence.Session.Code != "calendar_publication_pending" || r.SessionEvidence.Evidence.Quote.RegularSessionEligible) {
			t.Fatal("unpublished calendar did not suppress older", r)
		}
	}
}

func TestOutcomeQuoteSessionConflictingAndPreopenCalendars(t *testing.T) {
	for _, conflict := range []bool{false, true} {
		t.Run(map[bool]string{false: "preopen", true: "conflicting"}[conflict], func(t *testing.T) {
			s := outcomeMarketFixture(t)
			at := s.Basis.Decision.FinalizedAt
			outcomeCalendarFixture(t, &s, "first", `[{"date":"2026-10-21","open":"10:00","close":"16:00"}]`, at.Add(-time.Second), at.Add(-time.Second), at.Add(-time.Second))
			if conflict {
				outcomeCalendarFixture(t, &s, "conflict", `[{"date":"2026-10-21","open":"09:30","close":"16:00"}]`, at.Add(-time.Second), at.Add(-time.Second), at.Add(-time.Second))
			}
			out, err := inspectPaperOutcomeMarket(context.Background(), s)
			if err != nil {
				t.Fatal(err)
			}
			for _, r := range out.Rows {
				if r.CaptureKey == "spy:quote-r1" {
					if r.SessionEvidence == nil || r.SessionEvidence.Evidence.Quote.RegularSessionEligible {
						t.Fatal("preopen or conflicting source accepted", r)
					}
					if conflict && (r.SessionEvidence.Evidence.Session.Code != "conflicting_calendar_receipts" || len(r.SessionEvidence.CalendarSources) != 0) {
						t.Fatal(r.SessionEvidence)
					}
				}
			}
		})
	}
}
func TestOutcomeQuoteSessionRejectsCalendarAdmissionCorruption(t *testing.T) {
	s := outcomeMarketFixture(t)
	at := s.Basis.Decision.FinalizedAt
	outcomeCalendarFixture(t, &s, "calendar", `[{"date":"2026-10-21","open":"09:30","close":"16:00"}]`, at.Add(-time.Second), at.Add(-time.Second), at.Add(-time.Second))
	for id, a := range s.Basis.Observation.Feed.Collectors.Admissions {
		if s.Basis.Observation.Feed.Routing.Inputs[id].Input.Source == "alpaca-regular-session" {
			a.At = at.Add(-2 * time.Second)
			s.Basis.Observation.Feed.Collectors.Admissions[id] = a
		}
	}
	s.Outcome.BasisSHA256 = workerplan.Hash(s.Basis)
	s.Outcome.SHA256 = ""
	s.Outcome.SHA256 = workerplan.Hash(s.Outcome)
	if got, err := inspectPaperOutcomeMarket(context.Background(), s); err == nil || !reflect.DeepEqual(got, PaperOutcomeMarketEvidence{}) {
		t.Fatal("corrupt calendar admission accepted", got, err)
	}
}
