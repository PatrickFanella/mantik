package alpaca

import (
	"encoding/json"
	"errors"
	"slices"
	"time"
)

// CoverageSpec describes evidence required for one regular-session query. It
// cannot authorize execution, change retained quality, or configure readiness.
type CoverageSpec struct {
	Feed                  string `json:"feed"`
	Symbol                string `json:"symbol"`
	EvidenceClass         string `json:"evidence_class"`
	MaxQuoteAgeSeconds    int64  `json:"max_quote_age_seconds"`
	MaxCaptureAgeSeconds  int64  `json:"max_capture_age_seconds"`
	MaxCalendarAgeSeconds int64  `json:"max_calendar_age_seconds"`
}
type QuoteCoverage struct {
	CaptureID              string    `json:"capture_id,omitempty"`
	InputID                string    `json:"input_id,omitempty"`
	ReceivedAt             time.Time `json:"received_at,omitzero"`
	OccurredAt             time.Time `json:"occurred_at,omitzero"`
	Quality                string    `json:"quality"`
	Code                   string    `json:"code"`
	Usable                 bool      `json:"usable"`
	RegularSessionEligible bool      `json:"regular_session_eligible"`
}
type SessionCoverage struct {
	CaptureID  string    `json:"capture_id,omitempty"`
	ReceivedAt time.Time `json:"received_at,omitzero"`
	Date       string    `json:"date"`
	Status     string    `json:"status"`
	Code       string    `json:"code"`
	OpenAt     time.Time `json:"open_at,omitzero"`
	CloseAt    time.Time `json:"close_at,omitzero"`
	NextOpenAt time.Time `json:"next_open_at,omitzero"`
}
type CoverageReport struct {
	Scope                     string          `json:"scope"`
	At                        time.Time       `json:"at"`
	Spec                      CoverageSpec    `json:"spec"`
	Session                   SessionCoverage `json:"session"`
	Quote                     QuoteCoverage   `json:"quote"`
	PriceRequired             bool            `json:"price_required"`
	RequiredEvidenceSatisfied bool            `json:"required_evidence_satisfied"`
	CollectorLivenessVerified bool            `json:"collector_liveness_verified"`
	GlobalReadinessConfigured bool            `json:"global_readiness_configured"`
	QualificationCredit       bool            `json:"qualification_credit"`
}

func (s CoverageSpec) validate() error {
	if !slices.Contains([]string{"iex", "sip", "delayed_sip"}, s.Feed) || !symbolPattern.MatchString(s.Symbol) || !slices.Contains([]string{"synthetic_unqualified", "recorded_market_unqualified", "prospective_market_unqualified"}, s.EvidenceClass) || s.MaxQuoteAgeSeconds < 1 || s.MaxQuoteAgeSeconds > 86400 || s.MaxCaptureAgeSeconds < 1 || s.MaxCaptureAgeSeconds > 86400 || s.MaxCalendarAgeSeconds < 1 || s.MaxCalendarAgeSeconds > 604800 {
		return errors.New("coverage requires exact feed/symbol/evidence class and bounded positive age limits")
	}
	return nil
}

type sessionFact struct {
	Date            string
	OpenAt, CloseAt time.Time
}

func calendarFacts(c capture, symbol string) []sessionFact {
	ids := map[string]bool{}
	for _, input := range c.Result.Inputs {
		if input.Symbol == symbol {
			ids[input.ID] = true
		}
	}
	out := []sessionFact{}
	for _, session := range c.Result.Sessions {
		if ids[session.InputID] {
			out = append(out, sessionFact{session.Date, session.OpenAt, session.CloseAt})
		}
	}
	return out
}
func calendarIdentity(c capture, symbol string) string {
	raw, _ := json.Marshal(struct {
		Start, End string
		Facts      []sessionFact
	}{c.Recording.Start, c.Recording.End, calendarFacts(c, symbol)})
	return string(raw)
}
func quoteIdentity(c capture, symbol string) string {
	for _, input := range c.Result.Inputs {
		if input.Symbol == symbol {
			raw, _ := json.Marshal(struct {
				Kind, Quality, Reason string
				Price                 int64
				OccurredAt            time.Time
				MaxAge                int64
			}{input.Kind, input.Provenance.Quality, input.Provenance.Reason, input.PriceCents, input.OccurredAt, c.Recording.MaxQuoteAgeSeconds})
			return string(raw)
		}
	}
	return ""
}

// Coverage is a read-only, as-of evidence calculation. The CLI supplies actual
// wall time; injected timestamps in tests are not elapsed-time qualification.
// Latest unusable evidence suppresses older usable quotes. Equal-receipt
// conflicts are unknown rather than resolved by arbitrary capture names.
func (j *Journal) Coverage(s CoverageSpec, at time.Time) (CoverageReport, error) {
	out := CoverageReport{Scope: "local_regular_session_evidence", At: at.UTC(), Spec: s, Quote: QuoteCoverage{Quality: "missing", Code: "quote_capture_missing"}, Session: SessionCoverage{Status: "unknown", Code: "calendar_capture_missing"}}
	if err := s.validate(); err != nil {
		return out, err
	}
	if !validTime(at) {
		return out, errors.New("coverage requires a valid inspection time")
	}
	location, err := time.LoadLocation("America/New_York")
	if err != nil {
		return out, err
	}
	out.Session.Date = at.In(location).Format("2006-01-02")
	captures, err := j.captures()
	if err != nil {
		return out, err
	}
	var calendar, quote *capture
	calendarConflict, quoteConflict := false, false
	calendarFuture, quoteFuture := false, false
	for _, candidate := range captures {
		r := candidate.Recording
		if r.EvidenceClass != s.EvidenceClass || !slices.Contains(r.Symbols, s.Symbol) {
			continue
		}
		if r.Capability == "market_calendar" && r.Start <= out.Session.Date && r.End >= out.Session.Date {
			if r.ReceivedAt.After(at) {
				calendarFuture = true
				continue
			}
			if calendar == nil || r.ReceivedAt.After(calendar.Recording.ReceivedAt) {
				c := candidate
				calendar = &c
				calendarConflict = false
			} else if r.ReceivedAt.Equal(calendar.Recording.ReceivedAt) && calendarIdentity(candidate, s.Symbol) != calendarIdentity(*calendar, s.Symbol) {
				calendarConflict = true
			}
		}
		if r.Capability == "latest_quotes" && r.Feed == s.Feed {
			if r.ReceivedAt.After(at) {
				quoteFuture = true
				continue
			}
			if quote == nil || r.ReceivedAt.After(quote.Recording.ReceivedAt) {
				c := candidate
				quote = &c
				quoteConflict = false
			} else if r.ReceivedAt.Equal(quote.Recording.ReceivedAt) && quoteIdentity(candidate, s.Symbol) != quoteIdentity(*quote, s.Symbol) {
				quoteConflict = true
			}
		}
	}
	switch {
	case calendarFuture:
		out.Session.Code = "future_calendar_receipt"
	case calendarConflict:
		out.Session.Code = "conflicting_calendar_receipts"
	case calendar == nil:
	case at.Sub(calendar.Recording.ReceivedAt) > time.Duration(s.MaxCalendarAgeSeconds)*time.Second:
		out.Session.Code = "stale_calendar_capture"
	default:
		out.Session.CaptureID = calendar.Recording.CaptureID
		out.Session.ReceivedAt = calendar.Recording.ReceivedAt
		out.Session.Status, out.Session.Code = "closed", "calendar_no_session"
		for _, session := range calendarFacts(*calendar, s.Symbol) {
			if session.OpenAt.After(at) && (out.Session.NextOpenAt.IsZero() || session.OpenAt.Before(out.Session.NextOpenAt)) {
				out.Session.NextOpenAt = session.OpenAt
			}
			if session.Date != out.Session.Date {
				continue
			}
			out.Session.OpenAt, out.Session.CloseAt = session.OpenAt, session.CloseAt
			switch {
			case at.Before(session.OpenAt):
				out.Session.Code = "before_regular_open"
			case !at.Before(session.CloseAt):
				out.Session.Code = "after_regular_close"
			default:
				out.Session.Status, out.Session.Code = "open", "within_regular_session"
			}
		}
	}
	if quote != nil {
		out.Quote.CaptureID, out.Quote.ReceivedAt = quote.Recording.CaptureID, quote.Recording.ReceivedAt
		for _, input := range quote.Result.Inputs {
			if input.Symbol != s.Symbol {
				continue
			}
			out.Quote.InputID, out.Quote.OccurredAt, out.Quote.Quality = input.ID, input.OccurredAt, input.Provenance.Quality
			out.Quote.Code = input.Provenance.Reason
			if input.Kind == "quote" && input.Provenance.Quality == "usable" {
				out.Quote.Code = "usable_quote"
				switch {
				case at.Sub(quote.Recording.ReceivedAt) > time.Duration(s.MaxCaptureAgeSeconds)*time.Second:
					out.Quote.Code = "stale_quote_capture"
				case at.Sub(input.OccurredAt) > time.Duration(min(s.MaxQuoteAgeSeconds, quote.Recording.MaxQuoteAgeSeconds))*time.Second:
					out.Quote.Code = "stale_quote"
				default:
					out.Quote.Usable = true
				}
			}
		}
	}
	if quoteFuture {
		out.Quote.Usable = false
		out.Quote.Code = "future_quote_receipt"
	} else if quoteConflict {
		out.Quote.Usable = false
		out.Quote.Code = "conflicting_quote_receipts"
	}
	out.PriceRequired = out.Session.Status == "open"
	if out.PriceRequired && out.Quote.Usable {
		// A retained pre-open/previous-session quote cannot cover the current regular
		// session, even when a caller supplies a very permissive age bound.
		out.Quote.RegularSessionEligible = !out.Quote.OccurredAt.Before(out.Session.OpenAt) && out.Quote.OccurredAt.Before(out.Session.CloseAt)
	}
	out.RequiredEvidenceSatisfied = out.Session.Status == "closed" || out.PriceRequired && out.Quote.RegularSessionEligible
	return out, nil
}
