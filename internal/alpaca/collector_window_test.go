package alpaca

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"reflect"

	"git.subcult.tv/subculture-collective/mantik/internal/feedcollector"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// Keep at least one full minute open after provider minute precision is applied.
// Advancing the injected clock by two minutes must then reach the close boundary.
func collectorSessionCloseFixture(at time.Time) time.Time {
	return at.Add(2 * time.Minute).Truncate(time.Minute)
}

func TestCollectorSessionCloseFixtureMargin(t *testing.T) {
	zone, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Fatal(err)
	}
	for _, elapsed := range []time.Duration{0, 59 * time.Second, time.Minute - time.Nanosecond} {
		at := time.Date(2026, 10, 16, 11, 43, 0, 0, zone).Add(elapsed)
		closeAt := collectorSessionCloseFixture(at)
		if closeAt.Sub(at) < time.Minute || closeAt.After(at.Add(2*time.Minute)) || closeAt.Second() != 0 || closeAt.Nanosecond() != 0 {
			t.Fatalf("fixture lacks a full open minute or misses the injected stop: at=%s close=%s", at, closeAt)
		}
	}
}

func TestRegularCollectorCalendarFirstAndStopBoundaries(t *testing.T) {
	for _, mode := range []string{"completed", "closed", "calendar_failed", "calendar_publication_failed", "quote_unusable", "session_close", "deadline", "clock_backwards", "cancelled", "pacing_deadline"} {
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
						closeTime = collectorSessionCloseFixture(time.Now().In(zone)).Format("15:04")
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
				case "pacing_deadline":
					if _, ok := ctx.Deadline(); !ok {
						t.Error("pacing lost the run deadline")
					}
					return context.DeadlineExceeded
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
				expected := map[string]string{"closed": "regular_session_closed", "calendar_failed": "collector_failed", "calendar_publication_failed": "collector_failed", "quote_unusable": "quote_unusable", "session_close": "regular_session_closed", "deadline": "window_deadline", "clock_backwards": "clock_backwards", "cancelled": "cancelled", "pacing_deadline": "window_deadline"}[mode]
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

func TestRegularCollectorRealDeadlineBoundsWholeRun(t *testing.T) {
	for _, slowCalendar := range []bool{false, true} {
		t.Run(fmt.Sprintf("slow_calendar_%t", slowCalendar), func(t *testing.T) {
			j, s, control, receiver := collectorFixture(t)
			s.Session.IntervalSeconds = 1
			s.RegularSession = &RegularSessionWindow{MaxDurationSeconds: 1}
			zone, _ := time.LoadLocation("America/New_York")
			if at := time.Now().In(zone); at.Hour() == 23 && at.Minute() == 59 {
				t.Skip("manufactured same-day regular session ends at 23:59")
			}
			var calendars, quotes atomic.Int32
			client := testClient(t, func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/v2/calendar" {
					calendars.Add(1)
					if slowCalendar {
						<-r.Context().Done()
						return
					}
					_, _ = fmt.Fprintf(w, `[{"date":%q,"open":"00:00","close":"23:59"}]`, r.URL.Query().Get("start"))
					return
				}
				quotes.Add(1)
				_, _ = fmt.Fprint(w, freshQuote())
			})
			out, err := j.RunCollector(context.Background(), client, s, control, receiver)
			// The deadline starts at intent, so a loaded machine may expire during
			// calendar acquisition/publication, before any quote. Progress is not a
			// timing guarantee; stopped acquisition and truthful stop confirmation are.
			// Cleanup has its own finite deadline and can remain unconfirmed under load.
			if err == nil || out.State != "stopped" || out.StopCode != "window_deadline" || out.Published > 1 || calendars.Load() > 1 || quotes.Load() > 1 {
				t.Fatal(out, err, calendars.Load(), quotes.Load())
			}
			snapshot, snapshotErr := control.Snapshot(context.Background())
			if snapshotErr != nil {
				t.Fatal(snapshotErr)
			}
			lease := snapshot.State.Entries[s.Binding.CollectorRegistrationID].Lease
			if out.StopConfirmed && (lease == nil || !lease.Stopped) {
				t.Fatal("confirmed cleanup without a stopped lease", out, lease)
			}
			if lease != nil {
				assertCollectorLeaseExpires(t, snapshot, s)
			}
			beforeCalendar, beforeQuotes, beforeControl := calendars.Load(), quotes.Load(), control.calls
			replay, replayErr := j.RunCollector(context.Background(), client, s, control, receiver)
			if !errors.Is(replayErr, ErrCollectorStopped) || !reflect.DeepEqual(replay, out) || calendars.Load() != beforeCalendar || quotes.Load() != beforeQuotes || control.calls != beforeControl {
				t.Fatal("deadline replay repeated acquisition or cleanup", replay, replayErr)
			}
			if slowCalendar && (quotes.Load() != 0 || out.Published != 0) {
				t.Fatal("quote acquired after calendar deadline", out, quotes.Load())
			}
		})
	}
}

// A terminal acquisition record is not a claim that an unavailable control
// service acknowledged stop. A retained lease is finite even without that reply.
func assertCollectorLeaseExpires(t *testing.T, snapshot CollectorSnapshot, spec CollectorSpec) {
	t.Helper()
	entry := snapshot.State.Entries[spec.Binding.CollectorRegistrationID]
	lease := entry.Lease
	if lease == nil || lease.Generation == 0 || lease.OwnerID != spec.OwnerID || lease.ExpiresAt.After(lease.UpdatedAt.Add(300*time.Second)) || !lease.Stopped && !lease.ExpiresAt.Equal(lease.UpdatedAt.Add(300*time.Second)) {
		t.Fatal("invalid or unbounded retained lease", lease)
	}
	at := lease.ExpiresAt
	if at.Before(snapshot.State.LastClock) {
		at = snapshot.State.LastClock
	}
	expected := "expired"
	if lease.Stopped {
		expected = "stopped"
	}
	if status := feedcollector.LeaseStatus(entry, snapshot.State.LastClock, at); status != expected {
		t.Fatal("lease remained current at expiry", status, lease)
	}
	// Check the actual state machine, rather than waiting five minutes or
	// changing a real service clock. Neither terminal form can renew at expiry.
	_, _, err := feedcollector.Apply(snapshot.State, feedcollector.Command{RequestID: "deadline-expired-renew", Operation: "renew", RegistrationID: entry.Registration.ID, OwnerID: lease.OwnerID, Generation: lease.Generation}, at)
	if !errors.Is(err, feedcollector.ErrLeaseLost) {
		t.Fatal("expired/stopped generation renewed", err)
	}
}

type deadlineStopControl struct{ *testCollectorControl }

func (c deadlineStopControl) Command(ctx context.Context, receiver string, cmd feedcollector.Command) (feedcollector.Receipt, error) {
	if cmd.Operation == "stop" {
		c.calls++
		return feedcollector.Receipt{}, context.DeadlineExceeded
	}
	return c.testCollectorControl.Command(ctx, receiver, cmd)
}

func TestRegularCollectorDeadlineWithUnknownStopRetainsTruthAndNeverResumes(t *testing.T) {
	for _, mode := range []string{"unapplied_stop_timeout", "applied_stop_lost_reply"} {
		t.Run(mode, func(t *testing.T) {
			j, s, control, receiver := collectorFixture(t)
			s.Session.IntervalSeconds = 1
			s.RegularSession = &RegularSessionWindow{MaxDurationSeconds: 3600}
			zone, _ := time.LoadLocation("America/New_York")
			if at := time.Now().In(zone); at.Hour() == 23 && at.Minute() == 59 {
				t.Skip("manufactured same-day session ends at 23:59")
			}
			var backend CollectorControl = deadlineStopControl{control}
			if mode == "applied_stop_lost_reply" {
				control.lostStop = true
				backend = control
			}
			var calls atomic.Int32
			client := testClient(t, func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				if r.URL.Path == "/v2/calendar" {
					_, _ = fmt.Fprintf(w, `[{"date":%q,"open":"00:00","close":"23:59"}]`, r.URL.Query().Get("start"))
					return
				}
				_, _ = fmt.Fprint(w, freshQuote())
			})
			out, err := j.runCollectorAt(context.Background(), client, s, backend, receiver, func(context.Context, time.Duration) error { return context.DeadlineExceeded }, time.Now)
			if !errors.Is(err, ErrCollectorStopped) || out.State != "stopped" || out.StopCode != "window_deadline" || out.StopConfirmed || out.Published != 1 || calls.Load() != 2 {
				t.Fatal("unknown stop became successful completion", out, err, calls.Load())
			}
			snapshot, snapshotErr := control.Snapshot(context.Background())
			if snapshotErr != nil {
				t.Fatal(snapshotErr)
			}
			lease := snapshot.State.Entries[s.Binding.CollectorRegistrationID].Lease
			if lease == nil || lease.Stopped != (mode == "applied_stop_lost_reply") {
				t.Fatal("injected stop disposition lost", lease)
			}
			assertCollectorLeaseExpires(t, snapshot, s)
			// The last request must be the exact fenced stop attempt. A missing reply
			// stays missing; a later inspection may not manufacture a confirmation.
			var request collectorStep
			if _, err := j.readCollection(collectorName(s.Session.ID, fmt.Sprintf("step-%03d-request", out.Steps)), &request); err != nil || request.Command.Operation != "stop" || request.Command.Generation != lease.Generation {
				t.Fatal("stop intent not retained", request, err)
			}
			beforeCalls, beforeControl := calls.Load(), control.calls
			replay, replayErr := j.RunCollector(context.Background(), client, s, backend, receiver)
			if !errors.Is(replayErr, ErrCollectorStopped) || !reflect.DeepEqual(replay, out) || calls.Load() != beforeCalls || control.calls != beforeControl {
				t.Fatal("unknown stop was retried or resumed", replay, replayErr)
			}
			state, stateErr := receiver.store.Snapshot(context.Background())
			if stateErr != nil || state.ObservationCount != 0 || len(state.State.Actions.Items) != 0 {
				t.Fatal("collector reached fixture financial actions", stateErr)
			}
		})
	}
}
