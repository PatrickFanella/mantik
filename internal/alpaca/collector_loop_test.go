package alpaca

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

type loopScript struct {
	at     time.Time
	runs   []CollectorSpec
	stops  []string
	errs   []error
	waits  []time.Duration
	cancel context.CancelFunc
}

func (s *loopScript) loop() CollectorLoop {
	return CollectorLoop{
		Session: SessionSpec{ID: "spy", Count: 16, IntervalSeconds: 30}, WindowSeconds: 480,
		Binding: Binding{APIURL: "http://127.0.0.1:1", ReceiverID: "receiver", CollectorRegistrationID: "spy-iex"},
		Now:     func() time.Time { return s.at },
		Wait: func(_ context.Context, d time.Duration) error {
			s.waits = append(s.waits, d)
			s.at = s.at.Add(max(d, time.Second))
			return nil
		},
		Run: func(_ context.Context, spec CollectorSpec) (CollectorStatus, error) {
			s.runs = append(s.runs, spec)
			i := len(s.runs) - 1
			if i >= len(s.stops) {
				s.cancel()
				return CollectorStatus{StopCode: "cancelled"}, context.Canceled
			}
			var err error
			if i < len(s.errs) {
				err = s.errs[i]
			}
			return CollectorStatus{StopCode: s.stops[i]}, err
		},
	}
}

// Monday 10:00 New York.
var loopOpenClock = time.Date(2026, 11, 9, 15, 0, 0, 0, time.UTC)

func TestCollectorLoopRepeatsRunsWithFreshIdentities(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	s := &loopScript{at: loopOpenClock, cancel: cancel, stops: []string{"declared_count_completed", "window_deadline"}}
	if err := s.loop().RunCollectorLoop(ctx); err != nil {
		t.Fatal(err)
	}
	if len(s.runs) != 3 || s.waits[0] != 0 {
		t.Fatal(len(s.runs), s.waits)
	}
	seen := map[string]bool{}
	for _, spec := range s.runs {
		if seen[spec.Session.ID] || spec.OwnerID != spec.Session.ID || spec.RegularSession.MaxDurationSeconds != 480 || !strings.HasPrefix(spec.Session.ID, "spy-") || len(spec.Session.ID) > 31 {
			t.Fatal(spec)
		}
		seen[spec.Session.ID] = true
	}
}

// A closed market waits for the next open instead of reading every interval.
func TestCollectorLoopWaitsForTheNextOpen(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	// Friday 17:00 New York.
	friday := time.Date(2026, 11, 13, 22, 0, 0, 0, time.UTC)
	s := &loopScript{at: friday, cancel: cancel, stops: []string{"regular_session_closed"}}
	if err := s.loop().RunCollectorLoop(ctx); err != nil {
		t.Fatal(err)
	}
	monday := time.Date(2026, 11, 16, 14, 30, 0, 0, time.UTC)
	if len(s.waits) == 0 || friday.Add(s.waits[0]) != monday {
		t.Fatal(s.waits)
	}
}

func TestNextRegularOpen(t *testing.T) {
	zone, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct{ now, want time.Time }{
		{time.Date(2026, 11, 9, 8, 0, 0, 0, zone), time.Date(2026, 11, 9, 9, 30, 0, 0, zone)},
		{time.Date(2026, 11, 9, 9, 30, 0, 0, zone), time.Date(2026, 11, 10, 9, 30, 0, 0, zone)},
		{time.Date(2026, 11, 14, 12, 0, 0, 0, zone), time.Date(2026, 11, 16, 9, 30, 0, 0, zone)},
	} {
		if got := nextRegularOpen(c.now); !got.Equal(c.want) {
			t.Fatal(c.now, got, c.want)
		}
	}
}

func TestCollectorLoopStopsAfterRepeatedFailures(t *testing.T) {
	fault := errors.New("provider refused")
	s := &loopScript{at: loopOpenClock, cancel: func() {}, stops: []string{"", "", "", "", ""}, errs: []error{fault, fault, fault, fault, fault}}
	err := s.loop().RunCollectorLoop(t.Context())
	if !errors.Is(err, fault) || len(s.runs) != maxLoopFailures {
		t.Fatal(err, len(s.runs))
	}
	for _, d := range s.waits {
		if d != loopFailureDelay {
			t.Fatal(s.waits)
		}
	}
}

// A successful run clears the failure count.
func TestCollectorLoopRecoversBetweenFailures(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	fault := errors.New("transient")
	stops, errs := []string{}, []error{}
	for range 3 {
		stops, errs = append(stops, "", "", "", "", "declared_count_completed"), append(errs, fault, fault, fault, fault, nil)
	}
	s := &loopScript{at: loopOpenClock, cancel: cancel, stops: stops, errs: errs}
	if err := s.loop().RunCollectorLoop(ctx); err != nil || len(s.runs) != 16 {
		t.Fatal(err, len(s.runs))
	}
}

func TestCollectorLoopRejectsUnsafeConfiguration(t *testing.T) {
	s := &loopScript{at: loopOpenClock}
	for name, change := range map[string]func(*CollectorLoop){
		"long prefix": func(l *CollectorLoop) { l.Session.ID = strings.Repeat("a", 16) },
		"no window":   func(l *CollectorLoop) { l.WindowSeconds = 0 },
	} {
		l := s.loop()
		change(&l)
		if err := l.RunCollectorLoop(t.Context()); err == nil {
			t.Fatal(name)
		}
	}
	if len(s.runs) != 0 {
		t.Fatal("run started from invalid configuration")
	}
}
