package service

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/alpaca"
	"git.subcult.tv/subculture-collective/mantik/internal/domain"
	"git.subcult.tv/subculture-collective/mantik/internal/feedcollector"
	"git.subcult.tv/subculture-collective/mantik/internal/storage"
)

// Calendar sources pin both clocks and fences. Receipt time alone does not
// establish that calendar evidence was available to the service.
type PaperOutcomeCalendarSource struct {
	CaptureKey       string                          `json:"capture_key"`
	CaptureSHA256    string                          `json:"capture_sha256"`
	ReceivedAt       time.Time                       `json:"received_at"`
	PublishedAt      time.Time                       `json:"published_at"`
	AvailableAt      time.Time                       `json:"available_at"`
	PublicationFence feedcollector.Fence             `json:"publication_fence"`
	Admissions       []PaperOutcomeCalendarAdmission `json:"admissions"`
}
type PaperOutcomeCalendarAdmission struct {
	ReceivedAt  time.Time           `json:"received_at"`
	InputID     string              `json:"input_id"`
	InputSHA256 string              `json:"input_sha256"`
	At          time.Time           `json:"at"`
	Fence       feedcollector.Fence `json:"fence"`
}
type PaperOutcomeSessionEvidence struct {
	PendingCalendarAdmissions []PaperOutcomeCalendarAdmission `json:"pending_calendar_admissions"`
	Method                    string                          `json:"method"`
	Evidence                  domain.MarketCoverageReport     `json:"evidence"`
	CalendarSources           []PaperOutcomeCalendarSource    `json:"calendar_sources"`
}

// The caller already validated all source bytes against actual owner custody.
// Reconstruct only what was published AND admitted at this quote's availability
// cut. Newer admitted calendars without available originals still suppress older
// calendars. Other quotes cannot replace the exact row being inspected.
func inspectOutcomeQuoteSession(ctx context.Context, feed storage.FeedEvidenceSnapshot, registration string, row PaperOutcomeMarketRow) (PaperOutcomeSessionEvidence, error) {
	out := PaperOutcomeSessionEvidence{Method: "owned-quote-session-at-availability-v1", CalendarSources: []PaperOutcomeCalendarSource{}, PendingCalendarAdmissions: []PaperOutcomeCalendarAdmission{}}
	if err := ctx.Err(); err != nil {
		return PaperOutcomeSessionEvidence{}, err
	}
	if row.Quote == nil || row.AvailableAt.IsZero() {
		return PaperOutcomeSessionEvidence{}, storage.ErrPaperOutcomeInvalid
	}
	cut := feed
	cut.Records = map[string]storage.FeedEvidenceRecord{}
	cut.Collectors.Admissions = map[string]feedcollector.Admission{}
	for id, a := range feed.Collectors.Admissions {
		if a.Fence.RegistrationID != registration || a.At.After(row.AvailableAt) {
			continue
		}
		retained, ok := feed.Routing.Inputs[id]
		if !ok {
			return PaperOutcomeSessionEvidence{}, storage.ErrPaperOutcomeConflict
		}
		if id == row.InputID || retained.Input.Source == "alpaca-regular-session" {
			cut.Collectors.Admissions[id] = a
		}
	}
	candidates := map[string]PaperOutcomeCalendarSource{}
	publishedCalendars := map[string]bool{}
	for key, p := range feed.Records {
		if err := ctx.Err(); err != nil {
			return PaperOutcomeSessionEvidence{}, err
		}
		var r alpaca.Recording
		if json.Unmarshal(p.Recording, &r) != nil {
			return PaperOutcomeSessionEvidence{}, storage.ErrPaperOutcomeConflict
		}
		if r.Capability != "market_calendar" && key != row.CaptureKey {
			continue
		}
		if p.PublishedAt.After(row.AvailableAt) {
			continue
		}
		n, err := alpaca.Normalize(r)
		if err != nil {
			return PaperOutcomeSessionEvidence{}, err
		}
		source := PaperOutcomeCalendarSource{CaptureKey: key, CaptureSHA256: r.SHA256, ReceivedAt: r.ReceivedAt, PublishedAt: p.PublishedAt, AvailableAt: p.PublishedAt, PublicationFence: p.Fence, Admissions: []PaperOutcomeCalendarAdmission{}}
		available := true
		for _, i := range n.Inputs {
			a, ok := cut.Collectors.Admissions[i.ID]
			if !ok {
				available = false
				break
			}
			source.Admissions = append(source.Admissions, PaperOutcomeCalendarAdmission{InputID: i.ID, InputSHA256: a.InputSHA256, ReceivedAt: i.ReceivedAt, At: a.At, Fence: a.Fence})
			if a.At.After(source.AvailableAt) {
				source.AvailableAt = a.At
			}
		}
		if !available {
			continue
		}
		cut.Records[key] = p
		if r.Capability == "market_calendar" {
			candidates[key] = source
			for _, input := range n.Inputs {
				publishedCalendars[input.ID] = true
			}
		}
	}
	for id, a := range cut.Collectors.Admissions {
		input := feed.Routing.Inputs[id].Input
		if input.Source == "alpaca-regular-session" && !publishedCalendars[id] {
			out.PendingCalendarAdmissions = append(out.PendingCalendarAdmissions, PaperOutcomeCalendarAdmission{InputID: id, InputSHA256: a.InputSHA256, ReceivedAt: input.ReceivedAt, At: a.At, Fence: a.Fence})
		}
	}
	slices.SortFunc(out.PendingCalendarAdmissions, func(a, b PaperOutcomeCalendarAdmission) int { return strings.Compare(a.InputID, b.InputID) })
	report, err := inspectFeedCoverage(cut, registration, row.AvailableAt)
	if err != nil {
		return PaperOutcomeSessionEvidence{}, err
	}
	out.Evidence = report.Evidence
	out.Evidence.Scope = "service_owned_quote_session_at_availability"
	if out.Evidence.Quote.InputID != row.InputID {
		return PaperOutcomeSessionEvidence{}, storage.ErrPaperOutcomeConflict
	}
	// Pin the exact calendar selected by the shared deterministic method.
	// Conflicting calendars have no selected source and remain unknown.
	if out.Evidence.Session.CaptureID != "" {
		for key, source := range candidates {
			var r alpaca.Recording
			if json.Unmarshal(cut.Records[key].Recording, &r) != nil {
				return PaperOutcomeSessionEvidence{}, storage.ErrPaperOutcomeConflict
			}
			if r.CaptureID == out.Evidence.Session.CaptureID && r.ReceivedAt.Equal(out.Evidence.Session.ReceivedAt) {
				out.CalendarSources = append(out.CalendarSources, source)
			}
		}
	}
	slices.SortFunc(out.CalendarSources, func(a, b PaperOutcomeCalendarSource) int { return strings.Compare(a.CaptureKey, b.CaptureKey) })
	if err := ctx.Err(); err != nil {
		return PaperOutcomeSessionEvidence{}, err
	}
	return out, nil
}
