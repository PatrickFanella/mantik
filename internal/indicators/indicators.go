// Package indicators adapts the pinned MIT go-talib implementation to ordered,
// adjusted point-in-time bars. It has no data acquisition or order authority.
package indicators

import (
	"errors"
	"fmt"
	"math"
	"strconv"
	"time"

	"github.com/markcheno/go-talib"

	"git.subcult.tv/subculture-collective/mantik/internal/marketdata"
)

// ErrInput means a series is short, unordered, unavailable or numerically invalid.
var ErrInput = errors.New("invalid indicator input")

// Series contains only available outputs. Values[0] belongs to input index Start;
// earlier indexes are unavailable, never the library's zero-filled warm-up.
type Series struct {
	Start  int
	Values []float64
}

// Bands contains two-standard-deviation Bollinger bands with an SMA middle.
type Bands struct{ Upper, Middle, Lower Series }

func inputs(s marketdata.Series, period, start int) (high, low, close []float64, err error) {
	if period < 2 || len(s.Bars) <= start || s.AsOf.IsZero() || s.AssetID == "" {
		return nil, nil, nil, ErrInput
	}
	for _, a := range s.Applied {
		if a.KnownAt.IsZero() || a.KnownAt.After(s.AsOf) {
			return nil, nil, nil, ErrInput
		}
	}
	for i, b := range s.Bars {
		d, e := time.Parse(time.DateOnly, b.Date)
		if e != nil || d.After(s.AsOf) || i > 0 && b.Date <= s.Bars[i-1].Date || b.Raw.AvailableAt.IsZero() || b.Raw.AvailableAt.After(s.AsOf) || b.Raw.AssetID != s.AssetID {
			return nil, nil, nil, ErrInput
		}
		values := make([]float64, 4)
		for j, text := range []string{b.High, b.Low, b.Close, b.Open} {
			v, e := strconv.ParseFloat(text, 64)
			if e != nil || !finite(v) || v <= 0 {
				return nil, nil, nil, ErrInput
			}
			values[j] = v
		}
		if values[0] < values[1] || values[2] < values[1] || values[2] > values[0] || values[3] < values[1] || values[3] > values[0] {
			return nil, nil, nil, ErrInput
		}
		high = append(high, values[0])
		low = append(low, values[1])
		close = append(close, values[2])
	}
	return high, low, close, nil
}

func finite(v float64) bool { return !math.IsNaN(v) && !math.IsInf(v, 0) }
func result(values []float64, start int) (Series, error) {
	for _, v := range values[start:] {
		if !finite(v) {
			return Series{}, fmt.Errorf("%w: nonfinite output", ErrInput)
		}
	}
	return Series{Start: start, Values: values[start:]}, nil
}

// SMA starts at period-1, after period closes.
func SMA(s marketdata.Series, period int) (Series, error) {
	_, _, c, err := inputs(s, period, period-1)
	if err != nil {
		return Series{}, err
	}
	return result(talib.Sma(c, period), period-1)
}

// EMA starts at period-1, seeded with the first period closes' SMA.
func EMA(s marketdata.Series, period int) (Series, error) {
	_, _, c, err := inputs(s, period, period-1)
	if err != nil {
		return Series{}, err
	}
	return result(talib.Ema(c, period), period-1)
}

// RSI starts at period, after period daily changes, using Wilder smoothing.
func RSI(s marketdata.Series, period int) (Series, error) {
	_, _, c, err := inputs(s, period, period)
	if err != nil {
		return Series{}, err
	}
	return result(talib.Rsi(c, period), period)
}

// ATR starts at period and uses Wilder smoothing of true range.
func ATR(s marketdata.Series, period int) (Series, error) {
	h, l, c, err := inputs(s, period, period)
	if err != nil {
		return Series{}, err
	}
	return result(talib.Atr(h, l, c, period), period)
}

// BBands starts at period-1 and uses population price standard deviation.
func BBands(s marketdata.Series, period int) (Bands, error) {
	_, _, c, err := inputs(s, period, period-1)
	if err != nil {
		return Bands{}, err
	}
	u, m, l := talib.BBands(c, period, 2, 2, talib.SMA)
	upper, err := result(u, period-1)
	if err != nil {
		return Bands{}, err
	}
	middle, err := result(m, period-1)
	if err != nil {
		return Bands{}, err
	}
	lower, err := result(l, period-1)
	if err != nil {
		return Bands{}, err
	}
	return Bands{upper, middle, lower}, nil
}

// Volatility is unannualized rolling population standard deviation of simple
// daily returns close[t]/close[t-1]-1. It starts at bar index period.
func Volatility(s marketdata.Series, period int) (Series, error) {
	_, _, c, err := inputs(s, period, period)
	if err != nil {
		return Series{}, err
	}
	r := make([]float64, len(c)-1)
	for i := range r {
		r[i] = c[i+1]/c[i] - 1
		if !finite(r[i]) {
			return Series{}, ErrInput
		}
	}
	v, err := result(talib.StdDev(r, period, 1), period-1)
	v.Start = period
	return v, err
}
