package alpaca

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func coverageSpec() CoverageSpec {
	return CoverageSpec{Feed: "iex", Symbol: "SPY", EvidenceClass: "synthetic_unqualified", MaxQuoteAgeSeconds: 60, MaxCaptureAgeSeconds: 300, MaxCalendarAgeSeconds: 86400}
}
func coverageTime(t *testing.T, text string) time.Time {
	t.Helper()
	at, err := time.Parse(time.RFC3339, text)
	if err != nil {
		t.Fatal(err)
	}
	return at
}
func retainCoverageCalendar(t *testing.T, j *Journal, id, raw, start, end string, at time.Time) {
	t.Helper()
	r := recording(raw)
	r.CaptureID = id
	r.Capability = "market_calendar"
	r.Feed = "regular_session"
	r.Start, r.End = start, end
	r.ReceivedAt = at
	if _, err := j.Retain(r); err != nil {
		t.Fatal(err)
	}
}
func retainCoverageQuote(t *testing.T, j *Journal, id string, at, quoteAt time.Time, ask string) {
	t.Helper()
	raw := fmt.Sprintf(`{"quotes":{"SPY":{"t":%q,"ap":%s,"bp":100,"as":10,"bs":10}}}`, quoteAt.Format(time.RFC3339Nano), ask)
	r := recording(raw)
	r.CaptureID = id
	r.ReceivedAt = at
	if _, err := j.Retain(r); err != nil {
		t.Fatal(err)
	}
}
func TestCoverageOpenCloseBoundariesAndRequiredEvidence(t *testing.T) {
	j := testOpenJournal(t, filepath.Join(t.TempDir(), "journal"))
	received := coverageTime(t, "2026-10-14T13:29:00Z")
	retainCoverageCalendar(t, j, "calendar", `[{"date":"2026-10-14","open":"09:30","close":"16:00"},{"date":"2026-10-15","open":"09:30","close":"16:00"}]`, "2026-10-14", "2026-10-15", received)
	for _, tc := range []struct {
		at, status, code    string
		required, satisfied bool
	}{
		{"2026-10-14T13:29:59Z", "closed", "before_regular_open", false, true},
		{"2026-10-14T13:30:00Z", "open", "within_regular_session", true, false},
		{"2026-10-14T19:59:59Z", "open", "within_regular_session", true, false},
		{"2026-10-14T20:00:00Z", "closed", "after_regular_close", false, true},
	} {
		out, err := j.Coverage(coverageSpec(), coverageTime(t, tc.at))
		if err != nil || out.Session.Status != tc.status || out.Session.Code != tc.code || out.PriceRequired != tc.required || out.RequiredEvidenceSatisfied != tc.satisfied {
			t.Fatal(out, err)
		}
		if out.CollectorLivenessVerified || out.GlobalReadinessConfigured || out.QualificationCredit {
			t.Fatal("local evidence acquired qualification authority")
		}
	}
	now := coverageTime(t, "2026-10-14T13:30:10Z")
	retainCoverageQuote(t, j, "quote", now, now.Add(-time.Second), "100.01")
	out, err := j.Coverage(coverageSpec(), now)
	if err != nil || !out.Quote.Usable || !out.Quote.RegularSessionEligible || !out.RequiredEvidenceSatisfied {
		t.Fatal(out, err)
	}
}
func TestCoverageLatestUnusableNeverFallsBackAndAgeBoundsDoNotLoosen(t *testing.T) {
	j := testOpenJournal(t, filepath.Join(t.TempDir(), "journal"))
	now := coverageTime(t, "2026-10-14T15:00:00Z")
	retainCoverageCalendar(t, j, "calendar", `[{"date":"2026-10-14","open":"09:30","close":"16:00"}]`, "2026-10-14", "2026-10-14", now.Add(-time.Minute))
	retainCoverageQuote(t, j, "older-good", now.Add(-10*time.Second), now.Add(-11*time.Second), "100.01")
	retainCoverageQuote(t, j, "latest-zero", now, now, "0")
	out, err := j.Coverage(coverageSpec(), now)
	if err != nil || out.Quote.CaptureID != "latest-zero" || out.Quote.Usable || out.RequiredEvidenceSatisfied || out.Quote.Quality != "quarantined" {
		t.Fatal(out, err)
	}
	other := testOpenJournal(t, filepath.Join(t.TempDir(), "other"))
	retainCoverageCalendar(t, other, "calendar", `[{"date":"2026-10-14","open":"09:30","close":"16:00"}]`, "2026-10-14", "2026-10-14", now.Add(-time.Minute))
	retainCoverageQuote(t, other, "good", now, now, "100.01")
	spec := coverageSpec()
	spec.MaxQuoteAgeSeconds = 3600
	spec.MaxCaptureAgeSeconds = 3600
	out, err = other.Coverage(spec, now.Add(61*time.Second))
	if err != nil || out.Quote.Usable || out.Quote.Code != "stale_quote" {
		t.Fatal("capture's original quote-age bound loosened", out, err)
	}
	spec.MaxQuoteAgeSeconds = 60
	spec.MaxCaptureAgeSeconds = 1
	out, err = other.Coverage(spec, now.Add(2*time.Second))
	if err != nil || out.Quote.Code != "stale_quote_capture" || out.RequiredEvidenceSatisfied {
		t.Fatal(out, err)
	}
}
func TestCoverageMissingStaleFutureConflictAndExactScope(t *testing.T) {
	now := coverageTime(t, "2026-10-14T15:00:00Z")
	for _, fault := range []string{"missing", "wrong-date", "wrong-class", "stale", "future", "conflict"} {
		t.Run(fault, func(t *testing.T) {
			j := testOpenJournal(t, filepath.Join(t.TempDir(), "journal"))
			raw := `[{"date":"2026-10-14","open":"09:30","close":"16:00"}]`
			if fault != "missing" && fault != "wrong-date" {
				retainCoverageCalendar(t, j, "first", raw, "2026-10-14", "2026-10-14", now.Add(-time.Minute))
			}
			if fault == "wrong-date" {
				retainCoverageCalendar(t, j, "first", `[]`, "2026-10-15", "2026-10-15", now.Add(-time.Minute))
			}
			if fault == "future" {
				retainCoverageCalendar(t, j, "future", raw, "2026-10-14", "2026-10-14", now.Add(time.Minute))
			}
			if fault == "conflict" {
				retainCoverageCalendar(t, j, "conflicting", strings.Replace(raw, "16:00", "13:00", 1), "2026-10-14", "2026-10-14", now.Add(-time.Minute))
			}
			spec := coverageSpec()
			if fault == "stale" {
				spec.MaxCalendarAgeSeconds = 1
			}
			if fault == "wrong-class" {
				spec.EvidenceClass = "prospective_market_unqualified"
			}
			out, err := j.Coverage(spec, now)
			if err != nil || out.Session.Status != "unknown" || out.RequiredEvidenceSatisfied {
				t.Fatal(out, err)
			}
		})
	}
	j := testOpenJournal(t, filepath.Join(t.TempDir(), "scope"))
	retainCoverageQuote(t, j, "iex", now, now, "100.01")
	spec := coverageSpec()
	spec.Feed = "sip"
	out, err := j.Coverage(spec, now)
	if err != nil || out.Quote.Code != "quote_capture_missing" {
		t.Fatal("IEX covered SIP", out, err)
	}
	spec = coverageSpec()
	spec.Symbol = "QQQ"
	out, err = j.Coverage(spec, now)
	if err != nil || out.Quote.Code != "quote_capture_missing" {
		t.Fatal("SPY covered another symbol", out, err)
	}
}
func TestCoverageNewYorkDateDSTEarlyCloseAndNonTradingDay(t *testing.T) {
	for _, tc := range []struct{ date, at, open, close, status string }{
		{"2026-03-06", "2026-03-06T14:30:00Z", "09:30", "16:00", "open"},
		{"2026-03-09", "2026-03-09T13:30:00Z", "09:30", "16:00", "open"},
		{"2026-11-27", "2026-11-27T18:00:00Z", "09:30", "13:00", "closed"},
		{"2026-10-14", "2026-10-15T02:00:00Z", "09:30", "16:00", "closed"},
	} {
		j := testOpenJournal(t, filepath.Join(t.TempDir(), "journal"))
		now := coverageTime(t, tc.at)
		raw := fmt.Sprintf(`[{"date":%q,"open":%q,"close":%q}]`, tc.date, tc.open, tc.close)
		retainCoverageCalendar(t, j, "calendar", raw, tc.date, tc.date, now.Add(-time.Second))
		out, err := j.Coverage(coverageSpec(), now)
		if err != nil || out.Session.Date != tc.date || out.Session.Status != tc.status {
			t.Fatal(out, err)
		}
	}
	j := testOpenJournal(t, filepath.Join(t.TempDir(), "weekend"))
	now := coverageTime(t, "2026-10-17T15:00:00Z")
	retainCoverageCalendar(t, j, "weekend", `[]`, "2026-10-17", "2026-10-17", now.Add(-time.Second))
	out, err := j.Coverage(coverageSpec(), now)
	if err != nil || out.Session.Status != "closed" || out.Session.Code != "calendar_no_session" || !out.RequiredEvidenceSatisfied {
		t.Fatal(out, err)
	}
}
func TestCoveragePreOpenQuoteAndConflictingQuotesCannotCoverSession(t *testing.T) {
	now := coverageTime(t, "2026-10-14T13:30:10Z")
	j := testOpenJournal(t, filepath.Join(t.TempDir(), "journal"))
	retainCoverageCalendar(t, j, "calendar", `[{"date":"2026-10-14","open":"09:30","close":"16:00"}]`, "2026-10-14", "2026-10-14", now.Add(-time.Minute))
	retainCoverageQuote(t, j, "preopen", now, now.Add(-20*time.Second), "100.01")
	out, err := j.Coverage(coverageSpec(), now)
	if err != nil || !out.Quote.Usable || out.Quote.RegularSessionEligible || out.RequiredEvidenceSatisfied {
		t.Fatal(out, err)
	}
	retainCoverageQuote(t, j, "conflict", now, now, "100.02")
	out, err = j.Coverage(coverageSpec(), now)
	if err != nil || out.Quote.Code != "conflicting_quote_receipts" || out.RequiredEvidenceSatisfied {
		t.Fatal(out, err)
	}
	future := now.Add(time.Minute)
	retainCoverageQuote(t, j, "future", future, future, "100.01")
	out, err = j.Coverage(coverageSpec(), now)
	if err != nil || out.Quote.Code != "future_quote_receipt" || out.RequiredEvidenceSatisfied {
		t.Fatal(out, err)
	}
}
func TestCoverageInvalidScopeAndTamperFailWithoutMutation(t *testing.T) {
	for _, mutate := range []func(*CoverageSpec){func(s *CoverageSpec) { s.Feed = "" }, func(s *CoverageSpec) { s.EvidenceClass = "qualified" }, func(s *CoverageSpec) { s.MaxQuoteAgeSeconds = 0 }, func(s *CoverageSpec) { s.MaxCalendarAgeSeconds = 604801 }, func(s *CoverageSpec) { s.Symbol = "bad symbol" }} {
		spec := coverageSpec()
		mutate(&spec)
		if validateCoverageSpec(spec) == nil {
			t.Fatal("invalid coverage query admitted")
		}
	}
	j := testOpenJournal(t, filepath.Join(t.TempDir(), "journal"))
	before, err := j.Status()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := j.Coverage(coverageSpec(), time.Time{}); err == nil {
		t.Fatal("invalid clock admitted")
	}
	if _, err := j.Coverage(coverageSpec(), coverageTime(t, "2026-10-14T15:00:00Z")); err != nil {
		t.Fatal(err)
	}
	after, err := j.Status()
	if err != nil || before.Captures != after.Captures || before.Inputs != after.Inputs {
		t.Fatal("coverage mutated journal", err)
	}
	r := recording(goodQuotes)
	if _, err := j.Retain(r); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(j.root.Name(), captureName(r)), []byte("corrupt retained capture"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := j.Coverage(coverageSpec(), coverageTime(t, "2026-10-14T15:00:00Z")); err == nil {
		t.Fatal("coverage trusted corrupt capture")
	}

}
