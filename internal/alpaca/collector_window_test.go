package alpaca

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestRegularCollectorCalendarFirstAndStopBoundaries(t *testing.T) {
	for _, mode := range []string{"completed", "closed", "calendar_failed", "calendar_publication_failed", "quote_unusable", "session_close", "deadline", "clock_backwards", "cancelled"} {
		t.Run(mode, func(t *testing.T) {
			j, s, control, receiver := collectorFixture(t)
			s.Session.IntervalSeconds = 1
			s.RegularSession = &RegularSessionWindow{MaxDurationSeconds: 3600}
			zone, _ := time.LoadLocation("America/New_York")
			if at := time.Now().In(zone); at.Hour() == 23 && at.Minute() >= 58 {
				t.Skip("manufactured same-day open interval cannot cross New York midnight; pure calendar boundary tests cover rollover")
			}
			calls := []string{}
			client := testClient(t, func(w http.ResponseWriter, r *http.Request) {
				calls = append(calls, r.URL.Path)
				if r.URL.Path == "/v2/calendar" {
					if mode == "calendar_failed" {
						w.WriteHeader(503)
						return
					}
					if mode == "closed" {
						_, _ = fmt.Fprint(w, "[]")
						return
					}
					closeTime := "23:59"
					if mode == "session_close" {
						closeTime = time.Now().Add(time.Minute).In(zone).Format("15:04")
					}
					_, _ = fmt.Fprintf(w, `[{"date":%q,"open":"00:00","close":%q}]`, r.URL.Query().Get("start"), closeTime)
					return
				}
				if mode == "quote_unusable" {
					_, _ = fmt.Fprint(w, `{"quotes":{"SPY":{"t":"2026-10-15T14:00:00Z","ap":0,"bp":0,"as":0,"bs":0}}}`)
					return
				}
				if receiver.publications < 1 {
					t.Error("quote requested before calendar publication")
				}
				_, _ = fmt.Fprint(w, freshQuote())
			})
			receiver.publicationFailure = mode == "calendar_publication_failed"
			offset := time.Duration(0)
			now := func() time.Time { return time.Now().Add(offset) }
			pause := func(ctx context.Context, d time.Duration) error {
				switch mode {
				case "session_close":
					offset = 2 * time.Minute
				case "deadline":
					offset = 2 * time.Hour
				case "clock_backwards":
					offset = -time.Hour
				case "cancelled":
					return context.Canceled
				}
				return nil
			}
			out, err := j.runCollectorAt(context.Background(), client, s, control, receiver, pause, now)
			if !out.StopConfirmed || len(calls) == 0 || calls[0] != "/v2/calendar" {
				t.Fatal(out, err, calls)
			}
			if mode == "completed" {
				if err != nil || out.State != "completed" || out.Published != 2 || !out.CalendarPublished || out.Calendar == nil || out.Calendar.State != "retained" || out.StopCode != "declared_count_completed" || out.Delivery.Acknowledged != 3 || len(calls) != 3 {
					t.Fatal(out, err, calls)
				}
				before := len(calls)
				replay, replayErr := j.RunCollector(context.Background(), client, s, control, receiver)
				if replayErr != nil || replay.State != "completed" || replay.Calendar == nil || replay.Published != 2 || len(calls) != before {
					t.Fatal("completed replay repeated work", replay, replayErr)
				}
			} else {
				expected := map[string]string{"closed": "regular_session_closed", "calendar_failed": "collector_failed", "calendar_publication_failed": "collector_failed", "quote_unusable": "quote_unusable", "session_close": "regular_session_closed", "deadline": "window_deadline", "clock_backwards": "clock_backwards", "cancelled": "cancelled"}[mode]
				if err == nil || out.State != "stopped" || out.StopCode != expected {
					t.Fatal(out, err, calls)
				}
				if mode == "closed" || mode == "calendar_failed" || mode == "calendar_publication_failed" {
					for _, path := range calls {
						if path != "/v2/calendar" {
							t.Fatal("quote acquired without a published open calendar", calls)
						}
					}
				} else if out.Published != 1 {
					t.Fatal("window stop fetched more than one quote", out, calls)
				}
				before := len(calls)
				if _, err := j.RunCollector(context.Background(), client, s, control, receiver); !errors.Is(err, ErrCollectorStopped) || len(calls) != before {
					t.Fatal("stopped replay resumed work", err)
				}
			}
			state, _ := receiver.store.Snapshot(context.Background())
			if state.ObservationCount != 0 || len(state.State.Actions.Items) != 0 {
				t.Fatal("calendar collector reached fixture execution")
			}
		})
	}
}

func TestRegularCollectorRejectsCalendarOrphanBeforeClaim(t *testing.T) {
	j, s, control, receiver := collectorFixture(t)
	s.Session.IntervalSeconds = 1
	s.RegularSession = &RegularSessionWindow{MaxDurationSeconds: 60}
	spec := windowCalendarSpec(s, time.Now())
	if err := j.writeCollection(collectionName(spec.ID, "start-1"), ReadAttempt{Number: 1, State: "started", At: time.Now()}); err != nil {
		t.Fatal(err)
	}
	client := testClient(t, func(w http.ResponseWriter, r *http.Request) { t.Error("provider reached") })
	if _, err := j.RunCollector(context.Background(), client, s, control, receiver); !errors.Is(err, ErrJournalConflict) || control.calls != 0 {
		t.Fatal(err, control.calls)
	}
}

func TestRegularCollectorSpecBounds(t *testing.T) {
	_, s, _, _ := collectorFixture(t)
	for _, duration := range []int{-1, 0, 3601} {
		s.RegularSession = &RegularSessionWindow{MaxDurationSeconds: duration}
		if err := s.validate(); err == nil {
			t.Fatal(duration)
		}
	}
	s.RegularSession = &RegularSessionWindow{MaxDurationSeconds: 60}
	if err := s.validate(); err == nil || !strings.Contains(err.Error(), "30 second") {
		t.Fatal("long cadence accepted", err)
	}
}

func TestRegularCollectorRetainedCalendarReceiptRequired(t *testing.T) {
	j, s, control, receiver := collectorFixture(t)
	s.Session.IntervalSeconds = 1
	s.RegularSession = &RegularSessionWindow{MaxDurationSeconds: 60}
	client := testClient(t, func(w http.ResponseWriter, r *http.Request) { _, _ = fmt.Fprint(w, "[]") })
	out, err := j.RunCollector(context.Background(), client, s, control, receiver)
	if !errors.Is(err, ErrCollectorStopped) || !out.CalendarPublished || out.StopCode != "regular_session_closed" {
		t.Fatal(out, err)
	}
	if err := j.root.Remove(collectorName(s.Session.ID, "publication-calendar-receipt")); err != nil {
		t.Fatal(err)
	}
	if _, err := j.Collector(s.Session.ID); err == nil {
		t.Fatal("calendar publication proof discarded")
	}
}

func TestRegularCollectorRealDeadlineStopsPacing(t *testing.T) {
	j, s, control, receiver := collectorFixture(t)
	s.Session.IntervalSeconds = 1
	s.RegularSession = &RegularSessionWindow{MaxDurationSeconds: 1}
	zone, _ := time.LoadLocation("America/New_York")
	if at := time.Now().In(zone); at.Hour() == 23 && at.Minute() == 59 {
		t.Skip("manufactured same-day regular session ends at 23:59")
	}
	calls := 0
	client := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.URL.Path == "/v2/calendar" {
			_, _ = fmt.Fprintf(w, `[{"date":%q,"open":"00:00","close":"23:59"}]`, r.URL.Query().Get("start"))
			return
		}
		_, _ = fmt.Fprint(w, freshQuote())
	})
	out, err := j.RunCollector(context.Background(), client, s, control, receiver)
	if err == nil || out.StopCode != "window_deadline" || !out.StopConfirmed || !out.CalendarPublished || out.Published != 1 || calls != 2 {
		t.Fatal(out, err, calls)
	}
}
