package storage

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// MaxDailyBarWrite bounds one RecordDailyBars call.
const MaxDailyBarWrite = 20000

var (
	ErrInvalidDailyBar = errors.New("invalid daily bar")
	// ErrDailyBarOrder refuses a correction that would become available at or
	// before the revision it replaces, which would change earlier reads.
	ErrDailyBarOrder = errors.New("daily bar correction is not later than its predecessor")

	dailyBarAsset   = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9:._-]{0,63}$`)
	dailyBarSymbol  = regexp.MustCompile(`^[A-Z][A-Z0-9.]{0,15}$`)
	dailyBarDecimal = regexp.MustCompile(`^(0|[1-9][0-9]{0,11})(\.[0-9]{1,12})?$`)
)

// DailyBar is one raw (unadjusted) daily bar for one asset and session date.
// AssetID is the source's stable asset identity; tickers are reused, so Symbol
// is only the ticker the bar was reported under. Prices are exact decimal text.
//
// PublishedAt is when the source published the bar, ObservedAt is when Mantik
// fetched it, and AvailableAt is the earliest clock at which a reader may use
// it. Revision is assigned by the store: 1 for the first bar of a date, and one
// more for each correction.
type DailyBar struct {
	AssetID     string    `json:"asset_id"`
	Symbol      string    `json:"symbol"`
	Date        string    `json:"date"`
	Open        string    `json:"open"`
	High        string    `json:"high"`
	Low         string    `json:"low"`
	Close       string    `json:"close"`
	VWAP        string    `json:"vwap"`
	Volume      int64     `json:"volume"`
	TradeCount  int64     `json:"trade_count"`
	PublishedAt time.Time `json:"published_at"`
	ObservedAt  time.Time `json:"observed_at"`
	AvailableAt time.Time `json:"available_at"`
	Revision    int       `json:"revision"`
}

// DailyBarReceipt counts what one write did with its bars.
type DailyBarReceipt struct {
	Inserted  int `json:"inserted"`
	Corrected int `json:"corrected"`
	Unchanged int `json:"unchanged"`
}

// sameValues reports whether two bars carry the same reported values. Times
// are excluded: fetching an unchanged bar again is not a correction.
func (b DailyBar) sameValues(o DailyBar) bool {
	return b.Symbol == o.Symbol && b.Open == o.Open && b.High == o.High && b.Low == o.Low && b.Close == o.Close &&
		b.VWAP == o.VWAP && b.Volume == o.Volume && b.TradeCount == o.TradeCount
}

func dailyBarPrice(text string, positive bool) (string, bool) {
	if !dailyBarDecimal.MatchString(text) {
		return "", false
	}
	if strings.Contains(text, ".") {
		text = strings.TrimSuffix(strings.TrimRight(text, "0"), ".")
	}
	return text, !positive || text != "0"
}

// dailyBarTime normalizes to what both stores keep: UTC at microsecond precision.
func dailyBarTime(t time.Time) time.Time { return t.UTC().Truncate(time.Microsecond) }

// dailyBarWrite validates one write and returns its bars in asset and date
// order, with canonical decimals and times.
func dailyBarWrite(bars []DailyBar) ([]DailyBar, error) {
	if len(bars) < 1 || len(bars) > MaxDailyBarWrite {
		return nil, fmt.Errorf("%w: a write takes 1–%d bars", ErrInvalidDailyBar, MaxDailyBarWrite)
	}
	out := slices.Clone(bars)
	for i := range out {
		b := &out[i]
		day, err := time.Parse(time.DateOnly, b.Date)
		ok := err == nil && day.Year() >= 1900 && dailyBarAsset.MatchString(b.AssetID) && dailyBarSymbol.MatchString(b.Symbol) &&
			b.Volume >= 0 && b.TradeCount >= 0 && b.Revision == 0 && !b.PublishedAt.IsZero() && !b.ObservedAt.IsZero()
		for _, p := range []*string{&b.Open, &b.High, &b.Low, &b.Close} {
			var valid bool
			*p, valid = dailyBarPrice(*p, true)
			ok = ok && valid
		}
		var valid bool
		b.VWAP, valid = dailyBarPrice(b.VWAP, false)
		b.PublishedAt, b.ObservedAt, b.AvailableAt = dailyBarTime(b.PublishedAt), dailyBarTime(b.ObservedAt), dailyBarTime(b.AvailableAt)
		if !ok || !valid || b.AvailableAt.Before(b.PublishedAt) {
			return nil, fmt.Errorf("%w: bar %d", ErrInvalidDailyBar, i)
		}
	}
	slices.SortStableFunc(out, func(a, b DailyBar) int {
		return cmp.Or(cmp.Compare(a.AssetID, b.AssetID), cmp.Compare(a.Date, b.Date))
	})
	for i := 1; i < len(out); i++ {
		if out[i].AssetID == out[i-1].AssetID && out[i].Date == out[i-1].Date {
			return nil, fmt.Errorf("%w: %s repeats %s", ErrInvalidDailyBar, out[i].AssetID, out[i].Date)
		}
	}
	return out, nil
}

// dailyBarStep decides what a write does with one bar given the latest stored
// revision for its asset and date, if any.
func dailyBarStep(b *DailyBar, latest *DailyBar, r *DailyBarReceipt) (bool, error) {
	switch {
	case latest == nil:
		b.Revision = 1
		r.Inserted++
	case latest.sameValues(*b):
		r.Unchanged++
		return false, nil
	case !b.AvailableAt.After(latest.AvailableAt) || b.ObservedAt.Before(latest.ObservedAt):
		return false, fmt.Errorf("%w: %s %s", ErrDailyBarOrder, b.AssetID, b.Date)
	default:
		b.Revision = latest.Revision + 1
		r.Corrected++
	}
	return true, nil
}

func dailyBarRead(assetID string, asOf time.Time) (time.Time, error) {
	if !dailyBarAsset.MatchString(assetID) || asOf.IsZero() {
		return asOf, fmt.Errorf("%w: a read needs an asset and a clock", ErrInvalidDailyBar)
	}
	return dailyBarTime(asOf), nil
}

// RecordDailyBars stores a batch of bars, all or none. A bar whose values
// match the latest stored revision changes nothing; a bar with different
// values is appended as the next revision.
func (m *Memory) RecordDailyBars(ctx context.Context, bars []DailyBar) (DailyBarReceipt, error) {
	if err := ctx.Err(); err != nil {
		return DailyBarReceipt{}, err
	}
	bars, err := dailyBarWrite(bars)
	if err != nil {
		return DailyBarReceipt{}, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	var receipt DailyBarReceipt
	var add []DailyBar
	for i := range bars {
		var latest *DailyBar
		if revisions := m.dailyBars[bars[i].AssetID][bars[i].Date]; len(revisions) > 0 {
			latest = &revisions[len(revisions)-1]
		}
		write, err := dailyBarStep(&bars[i], latest, &receipt)
		if err != nil {
			return DailyBarReceipt{}, err
		}
		if write {
			add = append(add, bars[i])
		}
	}
	if m.dailyBars == nil {
		m.dailyBars = map[string]map[string][]DailyBar{}
	}
	for _, b := range add {
		if m.dailyBars[b.AssetID] == nil {
			m.dailyBars[b.AssetID] = map[string][]DailyBar{}
		}
		m.dailyBars[b.AssetID][b.Date] = append(m.dailyBars[b.AssetID][b.Date], b)
	}
	return receipt, nil
}

// DailyBarsAsOf returns one asset's bars in date order as a reader at asOf
// could have known them: for each date, the latest revision available at or
// before asOf. A bar available after asOf is never returned.
func (m *Memory) DailyBarsAsOf(ctx context.Context, assetID string, asOf time.Time) ([]DailyBar, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	asOf, err := dailyBarRead(assetID, asOf)
	if err != nil {
		return nil, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	out := []DailyBar{}
	for _, revisions := range m.dailyBars[assetID] {
		for i := len(revisions) - 1; i >= 0; i-- {
			if !revisions[i].AvailableAt.After(asOf) {
				out = append(out, revisions[i])
				break
			}
		}
	}
	slices.SortFunc(out, func(a, b DailyBar) int { return cmp.Compare(a.Date, b.Date) })
	return out, nil
}

const dailyBarColumns = `asset_id,to_char(bar_date,'YYYY-MM-DD'),revision,symbol,open_price::text,high_price::text,low_price::text,close_price::text,vwap::text,volume,trade_count,published_at,observed_at,available_at`

func scanDailyBars(rows pgx.Rows) ([]DailyBar, error) {
	defer rows.Close()
	out := []DailyBar{}
	for rows.Next() {
		var b DailyBar
		if err := rows.Scan(&b.AssetID, &b.Date, &b.Revision, &b.Symbol, &b.Open, &b.High, &b.Low, &b.Close, &b.VWAP, &b.Volume, &b.TradeCount, &b.PublishedAt, &b.ObservedAt, &b.AvailableAt); err != nil {
			return nil, err
		}
		b.PublishedAt, b.ObservedAt, b.AvailableAt = b.PublishedAt.UTC(), b.ObservedAt.UTC(), b.AvailableAt.UTC()
		out = append(out, b)
	}
	return out, rows.Err()
}

// RecordDailyBars stores a batch of bars in one transaction. See Memory.
func (p *Postgres) RecordDailyBars(ctx context.Context, bars []DailyBar) (DailyBarReceipt, error) {
	bars, err := dailyBarWrite(bars)
	if err != nil {
		return DailyBarReceipt{}, err
	}
	tx, err := p.pool.Begin(ctx)
	if err != nil {
		return DailyBarReceipt{}, err
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	var receipt DailyBarReceipt
	batch := &pgx.Batch{}
	// Bars are sorted by asset, so writers take the per-asset locks in one order.
	for start := 0; start < len(bars); {
		end := start
		for end < len(bars) && bars[end].AssetID == bars[start].AssetID {
			end++
		}
		asset := bars[start].AssetID
		if _, err = tx.Exec(ctx, "SELECT pg_advisory_xact_lock(hashtextextended('mantik_daily_bars:'||$1,0))", asset); err != nil {
			return DailyBarReceipt{}, err
		}
		rows, err := tx.Query(ctx, "SELECT DISTINCT ON (bar_date) "+dailyBarColumns+" FROM mantik_daily_bars WHERE asset_id=$1 AND bar_date BETWEEN $2::date AND $3::date ORDER BY bar_date,revision DESC", asset, bars[start].Date, bars[end-1].Date)
		if err != nil {
			return DailyBarReceipt{}, err
		}
		stored, err := scanDailyBars(rows)
		if err != nil {
			return DailyBarReceipt{}, err
		}
		latest := make(map[string]*DailyBar, len(stored))
		for i := range stored {
			latest[stored[i].Date] = &stored[i]
		}
		for i := start; i < end; i++ {
			b := &bars[i]
			write, err := dailyBarStep(b, latest[b.Date], &receipt)
			if err != nil {
				return DailyBarReceipt{}, err
			}
			if write {
				batch.Queue("INSERT INTO mantik_daily_bars(asset_id,bar_date,revision,symbol,open_price,high_price,low_price,close_price,vwap,volume,trade_count,published_at,observed_at,available_at) VALUES($1,$2::date,$3,$4,$5::numeric,$6::numeric,$7::numeric,$8::numeric,$9::numeric,$10,$11,$12,$13,$14)",
					b.AssetID, b.Date, b.Revision, b.Symbol, b.Open, b.High, b.Low, b.Close, b.VWAP, b.Volume, b.TradeCount, b.PublishedAt, b.ObservedAt, b.AvailableAt)
			}
		}
		start = end
	}
	if batch.Len() > 0 {
		if err = tx.SendBatch(ctx, batch).Close(); err != nil {
			return DailyBarReceipt{}, err
		}
	}
	return receipt, tx.Commit(ctx)
}

// DailyBarsAsOf returns one asset's bars as known at asOf. See Memory.
func (p *Postgres) DailyBarsAsOf(ctx context.Context, assetID string, asOf time.Time) ([]DailyBar, error) {
	asOf, err := dailyBarRead(assetID, asOf)
	if err != nil {
		return nil, err
	}
	rows, err := p.pool.Query(ctx, "SELECT DISTINCT ON (bar_date) "+dailyBarColumns+" FROM mantik_daily_bars WHERE asset_id=$1 AND available_at<=$2 ORDER BY bar_date,revision DESC", assetID, asOf)
	if err != nil {
		return nil, err
	}
	return scanDailyBars(rows)
}
