package alpaca

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"
)

const (
	// A loop's run identity is its prefix, a dash and a 15-character second
	// stamp, within the 31 characters a session identity can use alongside
	// its derived calendar capture.
	MaxCollectorLoopPrefix = 15
	maxLoopFailures        = 5
	loopFailureDelay       = time.Minute
)

// CollectorLoop repeats finite, calendar-first collector runs on one journal
// until its context ends. Each run gets its own session and owner identity,
// so its evidence stays as custodied as a hand-launched run's. Regular-session
// runs follow one another directly. A closed market waits for the next
// possible open. Repeated failures stop the loop rather than spending
// capacity, and journal or registry capacity ending is such a failure.
type CollectorLoop struct {
	Session       SessionSpec
	WindowSeconds int
	Binding       Binding
	Run           func(context.Context, CollectorSpec) (CollectorStatus, error)
	Report        func(CollectorStatus, error)
	Now           func() time.Time
	Wait          func(context.Context, time.Duration) error
}

func (l CollectorLoop) validate() error {
	if len(l.Session.ID) > MaxCollectorLoopPrefix || l.WindowSeconds < 1 || l.WindowSeconds > 3600 || l.Run == nil || l.Now == nil || l.Wait == nil {
		return errors.New("collector loop requires a prefix of at most 15 characters, a 1–3600 second window and its clock")
	}
	return nil
}

// RunCollectorLoop runs until the context ends, which is a clean stop, or
// until maxLoopFailures consecutive runs fail.
func (l CollectorLoop) RunCollectorLoop(ctx context.Context) error {
	if err := l.validate(); err != nil {
		return err
	}
	failures, previous := 0, ""
	for {
		if ctx.Err() != nil {
			return nil
		}
		id := fmt.Sprintf("%s-%s", l.Session.ID, l.Now().UTC().Format("20060102t150405"))
		if id == previous {
			if l.Wait(ctx, time.Second) != nil {
				return nil
			}
			continue
		}
		previous = id
		session := l.Session
		session.ID = id
		spec := CollectorSpec{Session: session, Binding: l.Binding, OwnerID: id, RegularSession: &RegularSessionWindow{MaxDurationSeconds: l.WindowSeconds}}
		status, err := l.Run(ctx, spec)
		if l.Report != nil {
			l.Report(status, err)
		}
		if ctx.Err() != nil {
			return nil
		}
		delay := time.Duration(0)
		switch status.StopCode {
		case "declared_count_completed", "window_deadline":
			failures = 0
		case "regular_session_closed":
			failures, delay = 0, nextRegularOpen(l.Now()).Sub(l.Now())
		default:
			failures, delay = failures+1, loopFailureDelay
			slog.Error("collector run failed", "run", id, "stop_code", status.StopCode, "error", err)
			if failures >= maxLoopFailures {
				return fmt.Errorf("collector loop stopped after %d consecutive failed runs: %w", failures, err)
			}
		}
		if l.Wait(ctx, delay) != nil {
			return nil
		}
	}
}

// nextRegularOpen is the next weekday 09:30 in New York. A market holiday
// costs one calendar read when the run started then finds the session closed.
func nextRegularOpen(now time.Time) time.Time {
	zone, err := time.LoadLocation("America/New_York")
	if err != nil {
		return now.Add(24 * time.Hour)
	}
	local := now.In(zone)
	open := time.Date(local.Year(), local.Month(), local.Day(), 9, 30, 0, 0, zone)
	if !local.Before(open) {
		open = open.AddDate(0, 0, 1)
	}
	for open.Weekday() == time.Saturday || open.Weekday() == time.Sunday {
		open = open.AddDate(0, 0, 1)
	}
	return open
}
