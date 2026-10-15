package alpaca

import (
	"context"
	"errors"
	"fmt"
	"os"
	"reflect"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/feedcollector"
)

// RegularSessionWindow opts a finite collector into calendar-first operation.
// The wall deadline starts at the retained intent; no overnight wait or catch-up.
type RegularSessionWindow struct {
	MaxDurationSeconds int `json:"max_duration_seconds"`
}

type windowStop string

func (s windowStop) Error() string { return string(s) }

func windowCalendarSpec(s CollectorSpec, at time.Time) CollectionSpec {
	calendar := s.Session.Collection
	calendar.ID = s.Session.ID + "-calendar"
	calendar.Capability = "market_calendar"
	zone, _ := time.LoadLocation("America/New_York")
	calendar.Start = at.In(zone).Format("2006-01-02")
	calendar.End = calendar.Start
	if s.RegularSession != nil {
		calendar.End = at.Add(time.Duration(s.RegularSession.MaxDurationSeconds) * time.Second).In(zone).Format("2006-01-02")
	}
	return calendar
}

func collectorCoverage(s CollectorSpec, at time.Time, records ...Recording) (CoverageReport, error) {
	return CoverageFromRecordings(CoverageSpec{Feed: s.Session.Collection.Feed, Symbol: s.Session.Collection.Symbols[0], EvidenceClass: "prospective_market_unqualified", MaxQuoteAgeSeconds: 60, MaxCaptureAgeSeconds: 300, MaxCalendarAgeSeconds: 86400}, at, records)
}

func checkRegularWindow(s CollectorSpec, started time.Time, calendar *Recording, at time.Time) error {
	if s.RegularSession == nil {
		return nil
	}
	if at.Before(started) {
		return windowStop("clock_backwards")
	}
	if !at.Before(started.Add(time.Duration(s.RegularSession.MaxDurationSeconds) * time.Second)) {
		return windowStop("window_deadline")
	}
	if calendar == nil {
		return windowStop("calendar_unavailable")
	}
	report, err := collectorCoverage(s, at, *calendar)
	if err != nil {
		return windowStop("calendar_unavailable")
	}
	if report.Session.Status == "closed" {
		return windowStop("regular_session_closed")
	}
	if report.Session.Status != "open" {
		return windowStop("calendar_unavailable")
	}
	return nil
}

func collectorWindowStopCode(err error, state string) string {
	if state == "completed" {
		return "declared_count_completed"
	}
	var stopped windowStop
	if errors.As(err, &stopped) {
		return string(stopped)
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "window_deadline"
	}
	if errors.Is(err, context.Canceled) {
		return "cancelled"
	}
	return "collector_failed"
}
func validWindowStop(code string) bool {
	switch code {
	case "declared_count_completed", "clock_backwards", "window_deadline", "calendar_unavailable", "regular_session_closed", "quote_unusable", "cancelled", "collector_failed":
		return true
	}
	return false
}

func (j *Journal) publishCollectorCapture(ctx context.Context, s CollectorSpec, bound collectorBoundReceiver, spec CollectionSpec, suffix string) (Recording, error) {
	records, err := j.PublicationRecordings()
	if err != nil {
		return Recording{}, err
	}
	for _, record := range records {
		if record.CaptureID != spec.ID || record.Revision != 1 {
			continue
		}
		req := collectorPublication{s.Binding.ReceiverID, bound.fence, record}
		if err := j.writeCollection(collectorName(s.Session.ID, "publication-"+suffix+"-request"), req); err != nil {
			return record, err
		}
		var receipt feedcollector.EvidenceReceipt
		for attempt := 0; attempt < 3; attempt++ {
			receipt, err = bound.receiver.Publish(ctx, req.ReceiverID, req.Fence, req.Recording)
			if err == nil || ctx.Err() != nil {
				break
			}
		}
		if err != nil {
			return record, err
		}
		if err := validatePublicationReceipt(record, bound.fence, receipt); err != nil {
			return record, err
		}
		if err := j.writeCollection(collectorName(s.Session.ID, "publication-"+suffix+"-receipt"), receipt); err != nil {
			return record, err
		}
		return record, nil
	}
	return Recording{}, ErrJournalConflict
}

func (j *Journal) inspectWindowCalendar(intent collectorIntent, done collectorResult, last *feedcollector.Receipt) (*CollectionStatus, int, error) {
	if (done.Published > 0 && !done.CalendarPublished) || !validWindowStop(done.StopCode) || (done.State == "completed") != (done.StopCode == "declared_count_completed") {
		return nil, 0, ErrJournalConflict
	}
	spec := windowCalendarSpec(intent.Spec, intent.At)
	calendar, err := j.Collection(spec.ID)
	if errors.Is(err, os.ErrNotExist) {
		if done.CalendarPublished || done.State == "completed" {
			return nil, 0, ErrJournalConflict
		}
		return nil, 0, nil
	}
	if err != nil {
		return nil, 0, err
	}
	var collection collectionIntent
	if _, err := j.readCollection(collectionName(spec.ID, "intent"), &collection); err != nil {
		return nil, 0, err
	}
	if !reflect.DeepEqual(collection.Spec, spec) || collection.At.Before(intent.At) {
		return nil, 0, ErrJournalConflict
	}
	if !done.CalendarPublished {
		if done.State == "completed" {
			return nil, 0, ErrJournalConflict
		}
		return &calendar, 0, nil
	}
	if calendar.State != "retained" || last == nil || last.Entry.Lease == nil {
		return nil, 0, ErrJournalConflict
	}
	var req collectorPublication
	var receipt feedcollector.EvidenceReceipt
	if _, err := j.readCollection(collectorName(intent.Spec.Session.ID, "publication-calendar-request"), &req); err != nil {
		return nil, 0, err
	}
	if _, err := j.readCollection(collectorName(intent.Spec.Session.ID, "publication-calendar-receipt"), &receipt); err != nil {
		return nil, 0, err
	}
	f := feedcollector.Fence{RegistrationID: intent.Registration.ID, OwnerID: intent.Spec.OwnerID, Generation: last.Entry.Lease.Generation}
	if req.ReceiverID != intent.Spec.Binding.ReceiverID || req.Fence != f || req.Recording.CaptureID != spec.ID || validatePublicationReceipt(req.Recording, f, receipt) != nil {
		return nil, 0, ErrJournalConflict
	}
	records, err := j.PublicationRecordings()
	if err != nil {
		return nil, 0, err
	}
	for _, record := range records {
		if !reflect.DeepEqual(record, req.Recording) {
			continue
		}
		normalized, err := Normalize(record)
		if err != nil {
			return nil, 0, err
		}
		return &calendar, len(normalized.Inputs), nil
	}
	return nil, 0, fmt.Errorf("%w: calendar publication capture is missing", ErrJournalConflict)
}
