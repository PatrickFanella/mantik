package service

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sync"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/alpaca"
	"git.subcult.tv/subculture-collective/mantik/internal/domain"
	"git.subcult.tv/subculture-collective/mantik/internal/marketdata"
)

// DailyBarStore supports durable catch-up without a separate progress cursor.
type DailyBarStore interface {
	marketdata.BarWriter
	marketdata.UniverseBarReader
}

// DailyBarSource retains original calendar and bar responses before use.
type DailyBarSource interface {
	Calendar(context.Context, string, string) ([]alpaca.Session, error)
	Bars(context.Context, string, time.Time, time.Time) (alpaca.DailyBars, error)
}

// DailyBarWorker owns one reviewed universe for its lifetime. The owner refreshes
// membership explicitly; a stale screen must not silently drop missing assets.
// It has no trading or model authority. Call Run from only one goroutine.
type DailyBarWorker struct {
	assets       []marketdata.BackfillAsset
	start        time.Time
	source       DailyBarSource
	calendarDate string
	refreshed    map[string]string
	calendar     []alpaca.Session
	mu           sync.RWMutex
	at           time.Time
	completed    time.Time
	sessions     []alpaca.Session
	missing      map[string]bool
	failure      bool
}

// NewDailyBarWorker validates and copies the reviewed collection universe.
func NewDailyBarWorker(assets []marketdata.BackfillAsset, start time.Time, source DailyBarSource) (*DailyBarWorker, error) {
	if source == nil || start.Format(time.DateOnly) != start.UTC().Format(time.DateOnly) || !start.Equal(start.UTC().Truncate(24*time.Hour)) {
		return nil, errors.New("daily bars require a source and UTC start date")
	}
	if len(assets) < 1 || len(assets) > 100000 {
		return nil, errors.New("daily bars require 1–100000 reviewed assets")
	}
	symbols, ids := map[string]bool{}, map[string]bool{}
	for _, a := range assets {
		if err := marketdata.ValidateBackfill([]marketdata.BackfillAsset{a}, start, time.Now().UTC().Add(-alpaca.RecentSIPDelay)); err != nil {
			return nil, err
		}
		if symbols[a.Symbol] || ids[a.AssetID] {
			return nil, errors.New("duplicate daily collection asset or symbol")
		}
		symbols[a.Symbol], ids[a.AssetID] = true, true
	}
	return &DailyBarWorker{assets: append([]marketdata.BackfillAsset(nil), assets...), start: start, source: source, refreshed: map[string]string{}}, nil
}

// Cycle obtains the exchange calendar, then fills every missing eligible session.
// A partial response never advances a cursor past a hole. Empty responses are
// retried. Writes retain actual receipt times, including on restart catch-up.
func (w *DailyBarWorker) Cycle(ctx context.Context, store DailyBarStore, now time.Time) (err error) {
	sessions := []alpaca.Session{}
	missing := map[string]bool{}
	defer func() {
		w.mu.Lock()
		defer w.mu.Unlock()
		w.at, w.completed, w.sessions, w.missing, w.failure = now, now, sessions, missing, err != nil
	}()
	loc, err := time.LoadLocation("America/New_York")
	if err != nil {
		return err
	}
	today, _ := time.Parse(time.DateOnly, now.In(loc).Format(time.DateOnly))

	from := w.start
	if w.calendarDate != "" && w.calendarDate <= today.Format(time.DateOnly) {
		from, _ = time.Parse(time.DateOnly, w.calendarDate)
		for _, s := range w.calendar {
			if s.Date < w.calendarDate {
				sessions = append(sessions, s)
			}
		}
	}
	// Completed calendar ranges are cached; refresh the last observed date
	// and any newly elapsed dates so today's early close remains current.
	for !from.After(today) {
		to := from.AddDate(0, 0, 30)
		if to.After(today) {
			to = today
		}
		days, e := w.source.Calendar(ctx, from.Format(time.DateOnly), to.Format(time.DateOnly))
		if e != nil {
			return e
		}
		prior := ""
		for _, s := range days {
			if s.Date < from.Format(time.DateOnly) || s.Date > to.Format(time.DateOnly) || s.Date <= prior || s.CloseAt.IsZero() || !s.CloseAt.After(s.OpenAt) {
				return errors.New("invalid daily collection calendar")
			}
			prior = s.Date
			sessions = append(sessions, s)
		}
		from = to.AddDate(0, 0, 1)
	}
	w.calendarDate, w.calendar = today.Format(time.DateOnly), append([]alpaca.Session(nil), sessions...)
	eligible := []alpaca.Session{}
	due := map[string]time.Time{}
	for _, s := range sessions {
		if !now.Before(s.CloseAt.Add(alpaca.RecentSIPDelay)) {
			eligible = append(eligible, s)
			due[s.Date] = s.CloseAt.Add(alpaca.RecentSIPDelay)
		}
	}
	if len(eligible) == 0 {
		return nil
	}
	last := eligible[len(eligible)-1]
	var failures []error
	for _, a := range w.assets {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		e := func() error {
			bars, e := store.DailyBarsAsOf(ctx, a.AssetID, now)
			if e != nil {
				return e
			}
			have := map[string]bool{}
			for _, b := range bars {
				if at, ok := due[b.Date]; ok && !b.ObservedAt.Before(at) {
					have[b.Date] = true
				}
			}
			first := ""
			for _, s := range eligible {
				if !have[s.Date] {
					if first == "" {
						first = s.Date
					}
					missing[a.AssetID+":"+s.Date] = true
				}
			}
			// Refresh the two most recent eligible sessions once per day/session
			// to append provider corrections, including overnight volume updates.
			key := today.Format(time.DateOnly) + ":" + last.Date
			if w.refreshed[a.AssetID] != key {
				refresh := eligible[max(0, len(eligible)-2)].Date
				if first == "" || refresh < first {
					first = refresh
				}
			}
			if first == "" {
				return nil
			}
			start, _ := time.Parse(time.DateOnly, first)
			// Fetch year chunks through the last eligible close.
			fetch := func(ctx context.Context, symbol string, from, to time.Time) (alpaca.DailyBars, error) {
				if to.After(last.CloseAt) {
					to = last.CloseAt
				}
				result, e := w.source.Bars(ctx, symbol, from, to)
				if e == nil {
					for _, b := range result.Bars[symbol] {
						delete(missing, a.AssetID+":"+b.Time.UTC().Format(time.DateOnly))
					}
				}
				return result, e
			}
			// A close is at least 15 minutes old; keep its request end fixed.
			if e = marketdata.BackfillDaily(ctx, store, fetch, []marketdata.BackfillAsset{a}, start, last.CloseAt, io.Discard); e != nil {
				return e
			}
			w.refreshed[a.AssetID] = key
			return nil
		}()
		if e != nil {
			failures = append(failures, e)
		}
	}
	if len(failures) > 0 {
		return errors.Join(failures...)
	}
	if len(missing) > 0 {
		return fmt.Errorf("daily bars missing for %d asset sessions", len(missing))
	}
	return nil
}

// Readiness checks bar coverage as well as cycle liveness. A cycle before
// today's close cannot stay ready when that session becomes due.
func (w *DailyBarWorker) Readiness(now time.Time) domain.CapabilityReadiness {
	cap := domain.CapabilityReadiness{Status: "not_configured", Code: "daily_bars_not_configured", EvidenceRefs: []string{}}
	if w == nil {
		return cap
	}
	w.mu.RLock()
	defer w.mu.RUnlock()
	cap.Status, cap.Code = "degraded", "daily_bars_stale"
	if w.at.IsZero() || now.Before(w.completed) || now.Sub(w.completed) > 3*time.Minute || w.failure || len(w.missing) > 0 {
		return cap
	}
	for _, s := range w.sessions {
		if !now.Before(s.CloseAt.Add(alpaca.RecentSIPDelay)) && w.at.Before(s.CloseAt.Add(alpaca.RecentSIPDelay)) {
			return cap
		}
	}
	cap.Ready, cap.Status, cap.Code = true, "ready", "daily_bars_current"
	return cap
}

// Run recovers missing sessions immediately and retries every minute. Shutdown
// waits for the current request through context cancellation.
func (w *DailyBarWorker) Run(ctx context.Context, store DailyBarStore, clock Clock) {
	for ctx.Err() == nil {
		cycle, cancel := context.WithTimeout(ctx, 10*time.Minute)
		_ = w.Cycle(cycle, store, clock.Now()) // readiness exposes failure; no provider body is logged
		w.mu.Lock()
		w.completed = clock.Now()
		w.mu.Unlock()
		cancel()
		if err := clock.Wait(ctx, time.Minute); err != nil {
			return
		}
	}
}
