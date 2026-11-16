package research

import (
	"context"
	"errors"
	"testing"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/storage"
)

func TestDeskObservationPrecedesAvailability(t *testing.T) {
	for _, delta := range []time.Duration{-time.Nanosecond, 0, time.Nanosecond} {
		t.Run(delta.String(), func(t *testing.T) {
			d := fixture()
			d.Data.Bars[0].ObservedAt = d.AsOf.Add(-time.Hour)
			d.Data.Bars[0].AvailableAt = d.Data.Bars[0].ObservedAt.Add(delta)
			j := &memoryJournal{}
			caller := &scripted{t: t, replies: []string{`{"tool":"done"}`}}
			result, err := Run(context.Background(), j, storage.NewMemory(), nil, caller, d)
			if delta < 0 {
				if !errors.Is(err, ErrInvalid) || caller.calls != 0 || len(j.events) != 0 {
					t.Fatalf("backdated availability reached the desk: calls=%d result=%+v error=%v", caller.calls, result, err)
				}
				return
			}
			if err != nil || caller.calls != 1 {
				t.Fatalf("valid observation/availability ordering refused: %+v %v", result, err)
			}
			before, err := d.Data.AdjustedBars(context.Background(), "SPY", d.Data.Bars[0].ObservedAt.Add(-time.Nanosecond))
			if err != nil {
				t.Fatal(err)
			}
			for _, b := range before.Bars {
				if b.Date == d.Data.Bars[0].Date {
					t.Fatal("bar visible before actual observation")
				}
			}
			between, err := d.Data.AdjustedBars(context.Background(), "SPY", d.Data.Bars[0].ObservedAt)
			expected := 1
			if delta > 0 {
				expected = 0
			}
			// Other bars have their original historical clocks and may be visible.
			found := 0
			for _, b := range between.Bars {
				if b.Date == d.Data.Bars[0].Date {
					found++
				}
			}
			if err != nil || found != expected {
				t.Fatalf("availability boundary: matching=%d want=%d error=%v", found, expected, err)
			}
		})
	}
}
