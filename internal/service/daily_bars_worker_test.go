package service

import (
	"context"
	"errors"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/alpaca"
	"git.subcult.tv/subculture-collective/mantik/internal/marketdata"
	"git.subcult.tv/subculture-collective/mantik/internal/storage"
)

type dailySourceFixture struct {
	sessions []alpaca.Session
	calls    int
	empty    bool
	fail     bool
	at       time.Time
}

func (f *dailySourceFixture) Calendar(_ context.Context, start, end string) ([]alpaca.Session, error) {
	if f.fail {
		return nil, errors.New("calendar unavailable")
	}
	out := []alpaca.Session{}
	for _, s := range f.sessions {
		if s.Date >= start && s.Date <= end {
			out = append(out, s)
		}
	}
	return out, nil
}
func (f *dailySourceFixture) Bars(_ context.Context, _ string, start, end time.Time) (alpaca.DailyBars, error) {
	f.calls++
	if f.fail {
		return alpaca.DailyBars{}, errors.New("read failed")
	}
	if end.After(f.at.Add(-15 * time.Minute)) {
		return alpaca.DailyBars{}, errors.New("embargo violated")
	}
	bars := []alpaca.Bar{}
	for _, s := range f.sessions {
		day, _ := time.Parse(time.DateOnly, s.Date)
		if !f.empty && !day.Before(start) && !day.After(end) {
			bars = append(bars, alpaca.Bar{Time: day, Open: "100", High: "101", Low: "99", Close: "100", VWAP: "100", Volume: 10})
		}
	}
	return alpaca.DailyBars{Pages: []alpaca.Response{{ReceivedAt: f.at}}, Bars: map[string][]alpaca.Bar{"SPY": bars}}, nil
}
func dailySession(date, close string) alpaca.Session {
	loc, _ := time.LoadLocation("America/New_York")
	o, _ := time.ParseInLocation("2006-01-02 15:04", date+" 09:30", loc)
	c, _ := time.ParseInLocation("2006-01-02 15:04", date+" "+close, loc)
	return alpaca.Session{Date: date, OpenAt: o, CloseAt: c}
}
func TestDailyBarsEmbargoCatchupRestartAndReadiness(t *testing.T) {
	for _, tc := range []struct{ name, date, close string }{
		{"standard", "2026-01-06", "16:00"}, {"early_close", "2025-11-28", "13:00"}, {"daylight_time", "2026-03-09", "16:00"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			s := dailySession(tc.date, tc.close)
			start, _ := time.Parse(time.DateOnly, tc.date)
			source := &dailySourceFixture{sessions: []alpaca.Session{s}, at: s.CloseAt.Add(15*time.Minute - time.Second)}
			w, err := NewDailyBarWorker([]marketdata.BackfillAsset{{Symbol: "SPY", AssetID: "spy"}}, start, source)
			if err != nil {
				t.Fatal(err)
			}
			store := storage.NewMemory()
			if err = w.Cycle(ctx, store, source.at); err != nil || source.calls != 0 {
				t.Fatal(err, source.calls)
			}
			source.at = source.at.Add(time.Second)
			if w.Readiness(source.at).Ready {
				t.Fatal("previous pre-close cycle covered due session")
			}
			source.empty = true
			if err = w.Cycle(ctx, store, source.at); err == nil || w.Readiness(source.at).Ready {
				t.Fatal("empty response counted as collected")
			}
			source.empty = false
			source.at = source.at.Add(time.Minute)
			if err = w.Cycle(ctx, store, source.at); err != nil {
				t.Fatal(err)
			}
			if !w.Readiness(source.at).Ready {
				t.Fatal(w.Readiness(source.at))
			}
			bars, err := store.DailyBarsAsOf(ctx, "spy", source.at)
			if err != nil || len(bars) != 1 || !bars[0].AvailableAt.Equal(source.at) {
				t.Fatal(bars, err)
			}
			w2, err := NewDailyBarWorker(w.assets, start, source)
			if err != nil {
				t.Fatal(err)
			}
			before := source.calls
			if err = w2.Cycle(ctx, store, source.at); err != nil || source.calls != before+1 {
				t.Fatal("restart correction refresh failed", err)
			}
			if w2.Readiness(source.at.Add(4 * time.Minute)).Ready {
				t.Fatal("stale heartbeat ready")
			}
			source.fail = true
			if err = w2.Cycle(ctx, store, source.at); err == nil || w2.Readiness(source.at).Ready {
				t.Fatal("calendar outage ready")
			}
		})
	}
}
func TestDailyBarsFillsHoleBeforeLatestAndWeekend(t *testing.T) {
	ctx := context.Background()
	sessions := []alpaca.Session{dailySession("2026-01-08", "16:00"), dailySession("2026-01-09", "16:00")}
	at := time.Date(2026, 1, 10, 22, 0, 0, 0, time.UTC)
	source := &dailySourceFixture{sessions: sessions, at: at}
	start, _ := time.Parse(time.DateOnly, "2026-01-08")
	w, err := NewDailyBarWorker([]marketdata.BackfillAsset{{Symbol: "SPY", AssetID: "spy"}}, start, source)
	if err != nil {
		t.Fatal(err)
	}
	store := storage.NewMemory()
	_, err = store.RecordDailyBars(ctx, []storage.DailyBar{{AssetID: "spy", Symbol: "SPY", Date: "2026-01-09", Open: "100", High: "101", Low: "99", Close: "100", VWAP: "100", Volume: 10, PublishedAt: at.Add(-time.Hour), ObservedAt: at.Add(-time.Hour), AvailableAt: at.Add(-time.Hour)}})
	if err != nil {
		t.Fatal(err)
	}
	if err = w.Cycle(ctx, store, at); err != nil {
		t.Fatal(err)
	}
	bars, _ := store.DailyBarsAsOf(ctx, "spy", at)
	if len(bars) != 2 || bars[1].Revision != 1 || !w.Readiness(at).Ready {
		t.Fatal(bars, w.Readiness(at))
	}
	if err = w.Cycle(ctx, store, at.Add(time.Minute)); err != nil || source.calls != 1 {
		t.Fatal(err, source.calls)
	}
}
func TestDailyBarsHTTPReadiness(t *testing.T) {
	req := httptest.NewRequest("GET", "/readyz?scope=daily_bars", nil)
	res := httptest.NewRecorder()
	Handler(storage.NewMemory(), "").ServeHTTP(res, req)
	if res.Code != 503 || !strings.Contains(res.Body.String(), "daily_bars_not_configured") {
		t.Fatal(res.Code, res.Body.String())
	}
}

func TestDailyBarsAcceptsUniverseBeyondOneRequest(t *testing.T) {
	assets := make([]marketdata.BackfillAsset, 101)
	for i := range assets {
		assets[i] = marketdata.BackfillAsset{Symbol: fmt.Sprintf("S%03d", i), AssetID: fmt.Sprintf("asset-%d", i)}
	}
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	if _, err := NewDailyBarWorker(assets, start, &dailySourceFixture{}); err != nil {
		t.Fatal("collection universe is not one request", err)
	}
	assets[100] = assets[0]
	if _, err := NewDailyBarWorker(assets, start, &dailySourceFixture{}); err == nil {
		t.Fatal("duplicate asset accepted")
	}
}

type dailyCompletionClock struct {
	start, finish time.Time
	calls         int
}

func (c *dailyCompletionClock) Now() time.Time {
	c.calls++
	if c.calls == 1 {
		return c.start
	}
	return c.finish
}
func (c *dailyCompletionClock) Wait(context.Context, time.Duration) error { return context.Canceled }
func TestDailyBarsLongCycleUsesCompletionHeartbeat(t *testing.T) {
	start := time.Date(2026, 1, 10, 22, 0, 0, 0, time.UTC)
	source := &dailySourceFixture{sessions: []alpaca.Session{dailySession("2026-01-09", "16:00")}, at: start}
	w, err := NewDailyBarWorker([]marketdata.BackfillAsset{{Symbol: "SPY", AssetID: "spy"}}, time.Date(2026, 1, 9, 0, 0, 0, 0, time.UTC), source)
	if err != nil {
		t.Fatal(err)
	}
	clock := &dailyCompletionClock{start: start, finish: start.Add(4 * time.Minute)}
	w.Run(context.Background(), storage.NewMemory(), clock)
	if !w.Readiness(clock.finish).Ready {
		t.Fatal("completed long catch-up appeared stale", w.Readiness(clock.finish))
	}
	if w.Readiness(clock.finish.Add(4 * time.Minute)).Ready {
		t.Fatal("completion heartbeat never expires")
	}
}
