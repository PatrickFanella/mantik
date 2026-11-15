package account

import "time"

// SettlementDate returns T+1 for US equities, skipping weekends, US banking
// holidays and Good Friday. Dates use UTC midnight. This calendar covers the
// roadmap's modern bar history (2016 onward); exceptional clearing closures
// can be supplied through the engine's SettlementDate override.
func SettlementDate(traded time.Time) time.Time {
	d := time.Date(traded.Year(), traded.Month(), traded.Day(), 0, 0, 0, 0, time.UTC)
	for {
		d = d.AddDate(0, 0, 1)
		if d.Weekday() != time.Saturday && d.Weekday() != time.Sunday && !holiday(d) {
			return d
		}
	}
}

func holiday(d time.Time) bool {
	y := d.Year()
	for _, h := range []time.Time{
		bankObserved(y, time.January, 1),
		nth(y, time.January, time.Monday, 3), nth(y, time.February, time.Monday, 3),
		easter(y).AddDate(0, 0, -2), nth(y, time.June, time.Monday, 1).AddDate(0, 0, -7),
		observed(y, time.July, 4), nth(y, time.September, time.Monday, 1),
		nth(y, time.October, time.Monday, 2), bankObserved(y, time.November, 11),
		nth(y, time.November, time.Thursday, 4), observed(y, time.December, 25),
	} {
		if d.Equal(h) {
			return true
		}
	}
	// National days of mourning in the supported historical window.
	if d.Format(time.DateOnly) == "2018-12-05" || d.Format(time.DateOnly) == "2025-01-09" {
		return true
	}
	return y >= 2022 && d.Equal(observed(y, time.June, 19))
}

// Banks do not observe Saturday holidays on Friday. For New Year's Day the
// equity exchanges follow that convention too.
func bankObserved(y int, m time.Month, day int) time.Time {
	d := time.Date(y, m, day, 0, 0, 0, 0, time.UTC)
	if d.Weekday() == time.Sunday {
		return d.AddDate(0, 0, 1)
	}
	return d
}

func observed(y int, m time.Month, day int) time.Time {
	d := time.Date(y, m, day, 0, 0, 0, 0, time.UTC)
	if d.Weekday() == time.Saturday {
		return d.AddDate(0, 0, -1)
	}
	if d.Weekday() == time.Sunday {
		return d.AddDate(0, 0, 1)
	}
	return d
}

func nth(y int, m time.Month, weekday time.Weekday, n int) time.Time {
	d := time.Date(y, m, 1, 0, 0, 0, 0, time.UTC)
	return d.AddDate(0, 0, (int(weekday)-int(d.Weekday())+7)%7+7*(n-1))
}

// Gregorian Easter (Meeus/Jones/Butcher), used only for Good Friday.
func easter(y int) time.Time {
	a, b, c := y%19, y/100, y%100
	d, e, f := b/4, b%4, (b+8)/25
	g := (b - f + 1) / 3
	h := (19*a + b - d - g + 15) % 30
	i, k := c/4, c%4
	l := (32 + 2*e + 2*i - h - k) % 7
	m := (a + 11*h + 22*l) / 451
	return time.Date(y, time.Month((h+l-7*m+114)/31), (h+l-7*m+114)%31+1, 0, 0, 0, 0, time.UTC)
}
